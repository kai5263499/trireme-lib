package tcbatch

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"text/template"

	"github.com/aporeto-inc/trireme-lib/controller/internal/enforcer/datapath/tun/utils/tuntap"
)

const (
	qdisctemplate      = `qdisc add dev {{.DeviceName}} {{if eq .Parent  "root" }} root {{else }} parent {{.Parent}} {{end}} handle {{.QdiscID}}: {{.QdiscType}} {{"\n"}}`
	classtemplate      = `class add dev {{.DeviceName}}  parent {{.Parent}}: classid {{.Parent}}:{{.ClassId}} {{.QdiscType}} {{if .AdditionalParams}} {{range .AdditionalParams}} {{.}} {{end}} {{end}}{{"\n"}}`
	filtertemplate     = `filter add dev {{.DeviceName}} parent {{.Parent}}: protocol ip {{if ge .Prio  0}} prio {{.Prio}} {{else}} handle {{.FilterID}}: {{end}} {{if .U32match}} {{.ConvertU32}} {{end}}  {{if .Cgroup}} cgroup {{end}} action skbedit queue_mapping {{.QueueID}}{{"\n"}}`
	metafiltertemplate = `filter add dev {{.DeviceName}} parent {{.Parent}}: handle {{.FilterID}} basic match {{if .MetaMatch}} {{.ConvertMeta}} {{end}} action skbedit queue_mapping {{.QueueID}}{{"\n"}}`
)

// Qdisc strcut represents a qdisc(htb only) in the tcbatch (batched tc)
type Qdisc struct {
	DeviceName string
	Parent     string
	QdiscID    string
	QdiscType  string
}

// Class represents a cgroup/prio class in tcbatch
type Class struct {
	DeviceName       string
	Parent           string
	ClassId          string
	QdiscType        string
	AdditionalParams []string
}

// U32match represent a U32 match in a filter
type U32match struct {
	matchsize string
	val       uint32
	mask      uint32
	offset    uint32
}

type Meta struct {
	markType  string
	mask      uint32
	val       uint32
	condition string
}

// FilterSkbAction represent a Filter with skbedit action which modifies the queue of the outgoing packet
type FilterSkbAction struct {
	DeviceName string
	Parent     string
	FilterID   string
	U32match   *U32match
	MetaMatch  *Meta
	Cgroup     bool
	Prio       int
	QueueID    string
}

// tcBatch holds data required to serialize a tcbatch constrcuted using Qdisc, Class and FilterSkbAction structures
type tcBatch struct {
	buf             *bytes.Buffer
	numQueues       uint16
	DeviceName      string
	CgroupHighBit   uint16
	CgroupStartMark uint16
}

// ConvertU32 is a helper fucntion to convert a U32 struct to a tc command format for u32 matches
func (f FilterSkbAction) ConvertU32() string {
	return "u32 match " + f.U32match.matchsize + " 0x" + strconv.FormatUint(uint64(f.U32match.val), 16) + " 0x" + strconv.FormatUint(uint64(f.U32match.mask), 16) + " at " + strconv.FormatUint(uint64(f.U32match.offset), 10)
}

func (m FilterSkbAction) ConvertMeta() string {
	return "'meta(" + m.MetaMatch.markType + " mask" + strconv.Itoa(int(m.MetaMatch.mask)) + " " + m.MetaMatch.condition + " " + strconv.Itoa(int(m.MetaMatch.val)) + ")'"
}

// NewTCBatch creates a new tcbatch struct
func NewTCBatch(numQueues uint16, DeviceName string, CgroupHighBit uint16, CgroupStartMark uint16) (*tcBatch, error) {
	if numQueues > 255 {
		return nil, fmt.Errorf("Invalid Queue Num. Queue num has to be less than 255")
	}

	if CgroupHighBit > 15 {
		return nil, fmt.Errorf("cgroup high bit has to between 0-15")
	}
	if CgroupStartMark+numQueues+1 > 2^16 {
		return nil, fmt.Errorf("Cgroupstartmark has to high value")
	}

	if len(DeviceName) == 0 || len(DeviceName) > tuntap.IFNAMSIZE {
		return nil, fmt.Errorf("Invalid DeviceName")
	}
	return &tcBatch{
		buf:             bytes.NewBuffer([]byte{}),
		numQueues:       numQueues + 1,
		DeviceName:      DeviceName,
		CgroupHighBit:   CgroupHighBit,
		CgroupStartMark: CgroupStartMark,
	}, nil
}

// Qdiscs converts qdisc struct to tc command strings
func (t *tcBatch) Qdiscs(qdiscs []Qdisc) error {
	tmpl := template.New("Qdisc")
	if tmpl, err := tmpl.Parse(qdisctemplate); err == nil {
		for _, qdisc := range qdiscs {
			if err := tmpl.Execute(t.buf, qdisc); err != nil {
				return err
			}
		}
	} else {
		return err
	}
	return nil
}

// Classes converts class struct to tc class command strings
func (t *tcBatch) Classes(classes []Class) error {
	tmpl := template.New("class")
	if tmpl, err := tmpl.Parse(classtemplate); err == nil {
		for _, class := range classes {
			if err := tmpl.Execute(t.buf, class); err != nil {
				return err
			}
		}
	} else {
		return err
	}
	return nil
}

// Filters converts FilterSkbAction struct to tc filter commands
func (t *tcBatch) Filters(filters []FilterSkbAction, filterTemplate string) error {
	tmpl := template.New("filters")
	if tmpl, err := tmpl.Parse(filterTemplate); err == nil {
		for _, filter := range filters {
			if err := tmpl.Execute(t.buf, filter); err != nil {
				return err
			}
		}
	} else {
		return err
	}
	return nil
}

// String provides string function for tcbatch
func (t *tcBatch) String() string {
	return t.buf.String()
}

// BuildInputTCBatchCommand builds a list of tc commands for input processes
func (t *tcBatch) BuildInputTCBatchCommand() error {
	qdisc := Qdisc{
		DeviceName: t.DeviceName,
		QdiscID:    "1",
		QdiscType:  "htb",
		Parent:     "root",
	}
	if err := t.Qdiscs([]Qdisc{qdisc}); err != nil {
		return fmt.Errorf("Received error %s while parsing qdisc", err)
	}
	qdiscID := 1
	handleID := 10
	filters := make([]FilterSkbAction, t.numQueues)
	for i := 0; i < int(t.numQueues); i++ {
		filters[i] = FilterSkbAction{
			DeviceName: t.DeviceName,
			Parent:     strconv.Itoa(qdiscID),
			FilterID:   strconv.Itoa(handleID),
			QueueID:    strconv.Itoa(i),
			Prio:       -1,
			Cgroup:     false,
			MetaMatch: &Meta{
				markType:  "nf_mark",
				mask:      0xff,
				val:       0x64,
				condition: "eq",
			},
		}
		handleID = handleID + 10
	}
	if err := t.Filters(filters, metafiltertemplate); err != nil {
		return fmt.Errorf("Received error %s while parsing filters", err)
	}
	return nil
}

// BuildOutputTCBatchCommand builds the list of tc commands required by the trireme-lib to setup a tc datapath
func (t *tcBatch) BuildOutputTCBatchCommand() error {
	numQueues := t.numQueues
	//qdiscs := make([]Qdisc, numQueues+1)
	qdisc := Qdisc{
		DeviceName: t.DeviceName,
		QdiscID:    "1",
		QdiscType:  "htb",
		Parent:     "root",
	}
	if err := t.Qdiscs([]Qdisc{qdisc}); err != nil {
		return fmt.Errorf("Received error %s while parsing qdisc", err)
	}

	filter := FilterSkbAction{
		DeviceName: t.DeviceName,
		Parent:     "1",
		FilterID:   "1",
		QueueID:    "0",
		Prio:       -1,
		Cgroup:     true,
	}
	if err := t.Filters([]FilterSkbAction{filter}, filtertemplate); err != nil {
		return fmt.Errorf("Received error %s while parsing filters", err)
	}

	classes := make([]Class, numQueues)
	for i := 1; i < int(numQueues); i++ {
		classes[i] = Class{
			DeviceName:       t.DeviceName,
			Parent:           "1",
			ClassId:          strconv.FormatUint(uint64(t.CgroupStartMark)+uint64(i), 16),
			QdiscType:        "htb",
			AdditionalParams: []string{"rate", "100000mbit"},
		}
	}
	if err := t.Classes(classes); err != nil {
		return fmt.Errorf("Received error %s while parsing classes", err)
	}

	qdiscs := make([]Qdisc, numQueues)
	initialqueueid := 10
	for i := 0; i < int(numQueues); i++ {

		qdiscs[i] = Qdisc{
			DeviceName: t.DeviceName,
			QdiscID:    strconv.Itoa(initialqueueid),
			QdiscType:  "htb",
			Parent:     "1:" + strconv.FormatUint(uint64(t.CgroupStartMark)+uint64(i), 16),
		}
		initialqueueid = initialqueueid + 10

	}

	if err := t.Qdiscs(qdiscs); err != nil {
		return fmt.Errorf("Received error %s while parsing qdisc", err)
	}
	filters := make([]FilterSkbAction, numQueues)
	qdiscID := 10
	for i := 0; i < int(numQueues); i++ {
		filters[i] = FilterSkbAction{
			DeviceName: t.DeviceName,
			Parent:     strconv.Itoa(qdiscID),
			FilterID:   strconv.Itoa(qdiscID),
			QueueID:    strconv.Itoa(i + 1),
			Prio:       1,
			Cgroup:     false,
			U32match: &U32match{
				matchsize: "u8",
				val:       0x40,
				mask:      0xf0,
				offset:    0,
			},
		}
		qdiscID = qdiscID + 10
	}
	if err := t.Filters(filters, filtertemplate); err != nil {
		return fmt.Errorf("Received error %s while parsing filters", err)
	}
	return nil
}

func (t *tcBatch) Execute() error {
	for {
		line, err := t.buf.ReadString('\n')
		if err != nil {
			break
		}

		if path, err := exec.LookPath("tc"); err != nil {
			return fmt.Errorf("Received error %s while trying to locate tc binary", err)

		} else {

			params := strings.Fields(line)
			cmd := exec.Command(path, params...)
			fmt.Println(line)
			if output, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("Error %s Executing Command %s", err, output)
			}
		}

	}

	return nil
}