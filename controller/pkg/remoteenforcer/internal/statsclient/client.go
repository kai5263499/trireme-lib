package statsclient

import (
	"context"
	"errors"
	"os"
	"time"

	"go.uber.org/zap"

	"github.com/aporeto-inc/trireme-lib/controller/constants"
	"github.com/aporeto-inc/trireme-lib/controller/internal/enforcer/utils/rpcwrapper"
	"github.com/aporeto-inc/trireme-lib/controller/pkg/remoteenforcer/internal/statscollector"
)

const (
	defaultStatsIntervalMiliseconds = 1000
	defaultUserRetention            = 10
	statsContextID                  = "UNUSED"
	statsRPCCommand                 = "StatsServer.GetStats"
)

// statsClient  This is the struct for storing state for the rpc client
// which reports flow stats back to the controller process
type statsClient struct {
	collector     statscollector.Collector
	rpchdl        *rpcwrapper.RPCWrapper
	secret        string
	statsChannel  string
	statsInterval time.Duration
	userRetention time.Duration
	stop          chan bool
}

// NewStatsClient initializes a new stats client
func NewStatsClient(cr statscollector.Collector) (StatsClient, error) {

	sc := &statsClient{
		collector:     cr,
		rpchdl:        rpcwrapper.NewRPCWrapper(),
		secret:        os.Getenv(constants.EnvStatsSecret),
		statsChannel:  os.Getenv(constants.EnvStatsChannel),
		statsInterval: defaultStatsIntervalMiliseconds * time.Millisecond,
		userRetention: defaultUserRetention * time.Minute,
		stop:          make(chan bool),
	}

	if sc.statsChannel == "" {
		return nil, errors.New("no path to stats socket provided")
	}

	if sc.secret == "" {
		return nil, errors.New("no secret provided for stats channel")
	}

	return sc, nil
}

// sendStats  async function which makes a rpc call to send stats every STATS_INTERVAL
func (s *statsClient) sendStats(ctx context.Context) {

	ticker := time.NewTicker(s.statsInterval)
	userTicker := time.NewTicker(s.userRetention)
	// nolint : gosimple
	for {
		select {
		case <-ticker.C:

			flows := s.collector.GetAllRecords()
			users := s.collector.GetUserRecords()
			if flows == nil && users == nil {
				continue
			}

			request := rpcwrapper.Request{
				Payload: &rpcwrapper.StatsPayload{
					Flows: flows,
					Users: users,
				},
			}

			if err := s.rpchdl.RemoteCall(
				statsContextID,
				statsRPCCommand,
				&request,
				&rpcwrapper.Response{},
			); err != nil {
				zap.L().Error("RPC failure in sending statistics: Unable to send flows")
			}
		case <-userTicker.C:
			s.collector.FlushUserCache()
		case <-ctx.Done():
			return
		}
	}

}

// Start This is an private function called by the remoteenforcer to connect back
// to the controller over a stats channel
func (s *statsClient) Run(ctx context.Context) error {

	if err := s.rpchdl.NewRPCClient(statsContextID, s.statsChannel, s.secret); err != nil {
		zap.L().Error("Stats RPC client cannot connect", zap.Error(err))
		return err
	}

	go s.sendStats(ctx)

	return nil
}
