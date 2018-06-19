package nfqdatapath

// Go libraries
import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"go.uber.org/zap"

	"github.com/aporeto-inc/netlink-go/conntrack"
	"github.com/aporeto-inc/trireme-lib/collector"
	"github.com/aporeto-inc/trireme-lib/common"
	"github.com/aporeto-inc/trireme-lib/controller/constants"
	"github.com/aporeto-inc/trireme-lib/controller/internal/enforcer/constants"
	"github.com/aporeto-inc/trireme-lib/controller/internal/enforcer/nfqdatapath/nflog"
	"github.com/aporeto-inc/trireme-lib/controller/internal/enforcer/nfqdatapath/tokenaccessor"
	"github.com/aporeto-inc/trireme-lib/controller/internal/portset"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/connection"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/fqconfig"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/packet"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/packetprocessor"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/pucontext"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/secrets"
	"github.com/aporeto-inc/trireme-lib/policy"
	"github.com/aporeto-inc/trireme-lib/utils/cache"
	"github.com/aporeto-inc/trireme-lib/utils/portcache"
	"github.com/aporeto-inc/trireme-lib/utils/portspec"
)

// DefaultExternalIPTimeout is the default used for the cache for External IPTimeout.
const DefaultExternalIPTimeout = "500ms"

// Datapath is the structure holding all information about a connection filter
type Datapath struct {

	// Configuration parameters
	filterQueue    *fqconfig.FilterQueue
	collector      collector.EventCollector
	tokenAccessor  tokenaccessor.TokenAccessor
	service        packetprocessor.PacketProcessor
	secrets        secrets.Secrets
	nflogger       nflog.NFLogger
	procMountPoint string

	// Internal structures and caches
	// Key=ContextId Value=puContext
	puFromContextID   cache.DataStore
	puFromMark        cache.DataStore
	contextIDFromPort *portcache.PortCache

	// For remotes this is a reverse link to the context
	puFromIP *pucontext.PUContext

	// Hash based on source IP/Port to capture SynAck packets with possible NAT.
	// When a new connection is created, we has the source IP/port. A return
	// poacket might come with a different source IP NAT is done later.
	// If we don't receife a return SynAck in 20 seconds, it expires
	sourcePortConnectionCache cache.DataStore

	// Hash on full five-tuple and return the connection
	// These are auto-expired connections after 60 seconds of inactivity.
	appOrigConnectionTracker    cache.DataStore
	appReplyConnectionTracker   cache.DataStore
	netOrigConnectionTracker    cache.DataStore
	netReplyConnectionTracker   cache.DataStore
	unknownSynConnectionTracker cache.DataStore

	// CacheTimeout used for Trireme auto-detecion
	ExternalIPCacheTimeout time.Duration

	// connctrack handle
	conntrackHdl conntrack.Conntrack

	// mode captures the mode of the enforcer
	mode constants.ModeType

	// ack size
	ackSize uint32

	mutualAuthorization bool
	packetLogs          bool

	portSetInstance portset.PortSet
}

// New will create a new data path structure. It instantiates the data stores
// needed to track sessions. The data path is started with a different call.
// Only required parameters must be provided. Rest a pre-populated with defaults.
func New(
	mutualAuth bool,
	filterQueue *fqconfig.FilterQueue,
	collector collector.EventCollector,
	serverID string,
	validity time.Duration,
	service packetprocessor.PacketProcessor,
	secrets secrets.Secrets,
	mode constants.ModeType,
	procMountPoint string,
	ExternalIPCacheTimeout time.Duration,
	packetLogs bool,
	tokenaccessor tokenaccessor.TokenAccessor,
	puFromContextID cache.DataStore,
) *Datapath {

	if ExternalIPCacheTimeout <= 0 {
		var err error
		ExternalIPCacheTimeout, err = time.ParseDuration(enforcerconstants.DefaultExternalIPTimeout)
		if err != nil {
			ExternalIPCacheTimeout = time.Second
		}
	}

	if mode == constants.RemoteContainer || mode == constants.LocalServer {
		// Make conntrack liberal for TCP

		sysctlCmd, err := exec.LookPath("sysctl")
		if err != nil {
			zap.L().Fatal("sysctl command must be installed", zap.Error(err))
		}

		cmd := exec.Command(sysctlCmd, "-w", "net.netfilter.nf_conntrack_tcp_be_liberal=1")
		if err := cmd.Run(); err != nil {
			zap.L().Fatal("Failed to set conntrack options", zap.Error(err))
		}

		cmd = exec.Command(sysctlCmd, "-w", "net.ipv4.ip_early_demux=0")
		if err := cmd.Run(); err != nil {
			// On some linux systems ip_early_demux does not
			// exist inside the network namespace. Even if this fails
			// we do not want to log it as an Error.
			zap.L().Info("Failed to set early demux options", zap.Error(err))
		}
	}

	// This cache is shared with portSetInstance. The portSetInstance
	// cleans up the entry corresponding to port when port is no longer
	// part of ipset portset.
	contextIDFromPort := portcache.NewPortCache("contextIDFromPort")

	var portSetInstance portset.PortSet

	if mode != constants.RemoteContainer {
		portSetInstance = portset.New(contextIDFromPort)
	}

	d := &Datapath{
		puFromMark:        cache.NewCache("puFromMark"),
		contextIDFromPort: contextIDFromPort,

		puFromContextID: puFromContextID,

		sourcePortConnectionCache:   cache.NewCacheWithExpiration("sourcePortConnectionCache", time.Second*24),
		appOrigConnectionTracker:    cache.NewCacheWithExpiration("appOrigConnectionTracker", time.Second*24),
		appReplyConnectionTracker:   cache.NewCacheWithExpiration("appReplyConnectionTracker", time.Second*24),
		netOrigConnectionTracker:    cache.NewCacheWithExpiration("netOrigConnectionTracker", time.Second*24),
		netReplyConnectionTracker:   cache.NewCacheWithExpiration("netReplyConnectionTracker", time.Second*24),
		unknownSynConnectionTracker: cache.NewCacheWithExpiration("unknownSynConnectionTracker", time.Second*2),
		ExternalIPCacheTimeout:      ExternalIPCacheTimeout,
		filterQueue:                 filterQueue,
		mutualAuthorization:         mutualAuth,
		service:                     service,
		collector:                   collector,
		tokenAccessor:               tokenaccessor,
		secrets:                     secrets,
		ackSize:                     secrets.AckSize(),
		mode:                        mode,
		procMountPoint:              procMountPoint,
		conntrackHdl:                conntrack.NewHandle(),
		portSetInstance:             portSetInstance,
		packetLogs:                  packetLogs,
	}

	packet.PacketLogLevel = packetLogs

	d.nflogger = nflog.NewNFLogger(11, 10, d.puInfoDelegate, collector)

	return d
}

// NewWithDefaults create a new data path with most things used by default
func NewWithDefaults(
	serverID string,
	collector collector.EventCollector,
	service packetprocessor.PacketProcessor,
	secrets secrets.Secrets,
	mode constants.ModeType,
	procMountPoint string,
) *Datapath {

	if collector == nil {
		zap.L().Fatal("Collector must be given to NewDefaultDatapathEnforcer")
	}

	defaultMutualAuthorization := false
	defaultFQConfig := fqconfig.NewFilterQueueWithDefaults()
	defaultValidity := time.Hour * 8760
	defaultExternalIPCacheTimeout, err := time.ParseDuration(enforcerconstants.DefaultExternalIPTimeout)
	if err != nil {
		defaultExternalIPCacheTimeout = time.Second
	}
	defaultPacketLogs := false

	tokenaccessor, err := tokenaccessor.New(serverID, defaultValidity, secrets)
	if err != nil {
		zap.L().Fatal("Cannot create a token engine")
	}

	puFromContextID := cache.NewCache("puFromContextID")

	return New(
		defaultMutualAuthorization,
		defaultFQConfig,
		collector,
		serverID,
		defaultValidity,
		service,
		secrets,
		mode,
		procMountPoint,
		defaultExternalIPCacheTimeout,
		defaultPacketLogs,
		tokenaccessor,
		puFromContextID,
	)
}

// Enforce implements the Enforce interface method and configures the data path for a new PU
func (d *Datapath) Enforce(contextID string, puInfo *policy.PUInfo) error {

	zap.L().Debug("Called Proxy Enforce")

	// Always create a new PU context
	pu, err := pucontext.NewPU(contextID, puInfo, d.ExternalIPCacheTimeout)
	if err != nil {
		return fmt.Errorf("error creating new pu: %s", err)
	}

	// Cache PUs for retrieval based on packet information
	if pu.Type() == common.LinuxProcessPU || pu.Type() == common.UIDLoginPU {
		mark, ports := pu.GetProcessKeys()
		d.puFromMark.AddOrUpdate(mark, pu)

		for _, port := range ports {
			portSpec, err := portspec.NewPortSpecFromString(port, contextID)
			if err != nil {
				continue
			}
			d.contextIDFromPort.AddPortSpec(portSpec)
		}
	} else {
		d.puFromIP = pu
	}

	// Cache PU from contextID for management and policy updates
	d.puFromContextID.AddOrUpdate(contextID, pu)

	return nil
}

// Unenforce removes the configuration for the given PU
func (d *Datapath) Unenforce(contextID string) error {

	puContext, err := d.puFromContextID.Get(contextID)
	if err != nil {
		return fmt.Errorf("contextid not found in enforcer: %s", err)
	}

	// Cleanup the IP based lookup
	pu := puContext.(*pucontext.PUContext)

	// Cleanup the mark information
	if err := d.puFromMark.Remove(pu.Mark()); err != nil {
		zap.L().Debug("Unable to remove cache entry during unenforcement",
			zap.String("Mark", pu.Mark()),
			zap.Error(err),
		)
	}

	// Cleanup the port cache
	for _, port := range pu.Ports() {
		if err := d.contextIDFromPort.RemoveStringPorts(port); err != nil {
			zap.L().Debug("Unable to remove cache entry during unenforcement",
				zap.String("Port", port),
				zap.Error(err),
			)
		}
	}

	// Cleanup the contextID cache
	if err := d.puFromContextID.RemoveWithDelay(contextID, 10*time.Second); err != nil {
		zap.L().Warn("Unable to remove context from cache",
			zap.String("contextID", contextID),
			zap.Error(err),
		)
	}

	return nil
}

// GetFilterQueue returns the filter queues used by the data path
func (d *Datapath) GetFilterQueue() *fqconfig.FilterQueue {

	return d.filterQueue
}

// GetPortSetInstance returns the portset instance used by data path
func (d *Datapath) GetPortSetInstance() portset.PortSet {

	return d.portSetInstance
}

// Run starts the application and network interceptors
func (d *Datapath) Run(ctx context.Context) error {

	zap.L().Debug("Start enforcer", zap.Int("mode", int(d.mode)))
	if d.service != nil {
		d.service.Initialize(d.secrets, d.filterQueue)
	}

	d.startApplicationInterceptor(ctx)
	d.startNetworkInterceptor(ctx)

	go d.nflogger.Run(ctx)

	return nil
}

// UpdateSecrets updates the secrets used for signing communication between trireme instances
func (d *Datapath) UpdateSecrets(token secrets.Secrets) error {

	d.secrets = token
	return d.tokenAccessor.SetToken(d.tokenAccessor.GetTokenServerID(), d.tokenAccessor.GetTokenValidity(), token)
}

func (d *Datapath) puInfoDelegate(contextID string) (ID string, tags *policy.TagStore) {

	item, err := d.puFromContextID.Get(contextID)
	if err != nil {
		return
	}

	ctx := item.(*pucontext.PUContext)

	ID = ctx.ManagementID()
	tags = ctx.Annotations().Copy()

	return
}

func (d *Datapath) reportFlow(p *packet.Packet, connection *connection.TCPConnection, sourceID string, destID string, context *pucontext.PUContext, mode string, report *policy.FlowPolicy, packet *policy.FlowPolicy) {

	c := &collector.FlowRecord{
		ContextID: context.ID(),
		Source: &collector.EndPoint{
			ID:   sourceID,
			IP:   p.SourceAddress.String(),
			Port: p.SourcePort,
			Type: collector.EnpointTypePU,
		},
		Destination: &collector.EndPoint{
			ID:   destID,
			IP:   p.DestinationAddress.String(),
			Port: p.DestinationPort,
			Type: collector.EnpointTypePU,
		},
		Tags:       context.Annotations(),
		Action:     report.Action,
		DropReason: mode,
		PolicyID:   report.PolicyID,
		L4Protocol: p.IPProto,
	}

	if report.ObserveAction.Observed() {
		c.ObservedAction = packet.Action
		c.ObservedPolicyID = packet.PolicyID
	}

	d.collector.CollectFlowEvent(c)
}
