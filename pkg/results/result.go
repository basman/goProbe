package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/els0r/goProbe/pkg/types"
)

var (
	ErrorNoResults = errors.New("query returned no results")
)

// Result bundles the data rows returned and the query meta information
type Result struct {
	Status        Status        `json:"status"`
	HostsStatuses HostsStatuses `json:"hosts_statuses"`

	Summary Summary `json:"summary"`
	Query   Query   `json:"query"`
	Rows    Rows    `json:"rows"`
}

// Query stores the kind of query that was run
type Query struct {
	Attributes []string `json:"attributes"`
	Condition  string   `json:"condition,omitempty"`
}

// Summary stores the total traffic volume and packets observed over the
// queried range and the interfaces that were queried
type Summary struct {
	Interfaces []string       `json:"interfaces"`
	TimeFirst  time.Time      `json:"time_first"`
	TimeLast   time.Time      `json:"time_last"`
	Totals     types.Counters `json:"totals"`
	Timings    Timings        `json:"timings"`
	Hits       Hits           `json:"hits"`
}

type Status struct {
	Code    types.Status `json:"code"`
	Message string       `json:"message,omitempty"`
}

func New() *Result {
	return &Result{
		Status: Status{
			Code: types.StatusOK,
		},
	}
}

func (r *Result) Start() {
	r.Summary = Summary{
		Timings: Timings{
			QueryStart: time.Now(),
		},
	}
	r.HostsStatuses = make(HostsStatuses)
}

func (r *Result) End() {
	r.Summary.Timings.QueryDuration = time.Since(r.Summary.Timings.QueryStart)
	if len(r.Rows) == 0 {
		r.Status = Status{
			Code:    types.StatusEmpty,
			Message: ErrorNoResults.Error(),
		}
	}
	sort.Strings(r.Summary.Interfaces)
}

// HostsStatus captures the query status for every host queried
type HostsStatuses map[string]Status

func (hs HostsStatuses) Print(w io.Writer) {
	var hosts []struct {
		host string
		Status
	}

	var ok, empty, withError int
	for host, status := range hs {
		switch status.Code {
		case types.StatusOK:
			ok++
		case types.StatusEmpty:
			empty++
		case types.StatusError:
			withError++
		}
		hosts = append(hosts, struct {
			host string
			Status
		}{host: host, Status: status})
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		return hosts[i].host < hosts[j].host
	})

	fmt.Fprintf(w, "Hosts: %d ok / %d empty / %d error\n\n", ok, empty, withError)

	tw := tabwriter.NewWriter(w, 0, 0, 4, ' ', tabwriter.AlignRight)

	sep := "\t"

	header := []string{"#", "host", "status", "message"}
	fmtStr := sep + strings.Join([]string{"%d", "%s", "%s", "%s"}, sep) + sep + "\n"

	fmt.Fprintln(tw, sep+strings.Join(header, sep)+sep)
	// fmt.Fprintln(tw, sep+strings.Repeat(sep, len(header))+sep)

	for i, host := range hosts {
		fmt.Fprintf(tw, fmtStr, i+1, host.host, host.Code, host.Message)
	}
	tw.Flush()
}

// Timinigs summarizes query runtimes
type Timings struct {
	QueryStart         time.Time     `json:"query_start"`
	QueryDuration      time.Duration `json:"query_duration"`
	ResolutionDuration time.Duration `json:"resolution,omitempty"`
}

// Hits stores how many flow records were returned in total and how many are
// returned in Rows
type Hits struct {
	Displayed int `json:"displayed"`
	Total     int `json:"total"`
}

// String prints the statistics
func (h Hits) String() string {
	return fmt.Sprintf("{total: %d, displayed: %d}", h.Total, h.Displayed)
}

// Row is a human-readable, aggregatable representation of goDB's data
type Row struct {
	// Partition Attributes
	Labels Labels `json:"l,omitempty"`

	// Attributes which can be grouped by
	Attributes Attributes `json:"a"`

	// Counters
	Counters types.Counters `json:"c"`
}

// String prints a single result
func (r *Row) String() string {
	return fmt.Sprintf("%s; %s; %s", r.Labels.String(), r.Attributes.String(), r.Counters.String())
}

// Less returns wether the row r sorts before r2
func (r *Row) Less(r2 *Row) bool {
	if r.Attributes == r2.Attributes {
		return r.Labels.Less(r2.Labels)
	}
	return r.Attributes.Less(r2.Attributes)
}

// Labels hold labels by which the goDB database is partitioned
type Labels struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	Iface     string    `json:"iface,omitempty"`
	Hostname  string    `json:"host,omitempty"`
	HostID    string    `json:"host_id,omitempty"`
}

// MarshalJSON implements the json.Marshaler interface. It makes sure
// that empty timestamps don't show up in the json output
func (l Labels) MarshalJSON() ([]byte, error) {
	var aux = struct {
		// TODO: this is expensive. Check how to get rid of re-assigning
		// values in order to properly treat empties
		Timestamp *time.Time `json:"timestamp,omitempty"`
		Iface     string     `json:"iface,omitempty"`
		Hostname  string     `json:"host,omitempty"`
		HostID    string     `json:"host_id,omitempty"`
	}{
		nil,
		l.Iface,
		l.Hostname,
		l.HostID,
	}
	if !l.Timestamp.IsZero() {
		aux.Timestamp = &l.Timestamp
	}
	return json.Marshal(aux)
}

// String prints all result labels
func (l Labels) String() string {
        return fmt.Sprintf("ts=%s iface=%s hostname=%s hostID=%s",
                l.Timestamp,
                l.Iface,
                l.Hostname,
                l.HostID,
        )
}

// Less returns wether the set of labels l sorts before l2
func (l Labels) Less(l2 Labels) bool {
	if l.Timestamp != l2.Timestamp {
		return l.Timestamp.Before(l2.Timestamp)
	}

	// Since sorting is about human-readable information this ignores the hostID, assuming
	// that for sorting identical hostnames imply the same host
	if l.Hostname != l2.Hostname {
		return l.Hostname < l2.Hostname
	}

	return l.Iface < l2.Iface
}

// Attributes are traffic attributes by which the goDB can be aggregated
type Attributes struct {
	SrcIP   netip.Addr `json:"sip,omitempty"`
	DstIP   netip.Addr `json:"dip,omitempty"`
	IPProto uint8      `json:"proto,omitempty"`
	DstPort uint16     `json:"dport,omitempty"`
}

func (a Attributes) MarshalJSON() ([]byte, error) {
	var aux = struct {
		// TODO: this is expensive. Check how to get rid of re-assigning
		// values in order to properly treat empties
		SrcIP   *netip.Addr `json:"sip,omitempty"`
		DstIP   *netip.Addr `json:"dip,omitempty"`
		IPProto uint8       `json:"proto,omitempty"`
		DstPort uint16      `json:"dport,omitempty"`
	}{
		IPProto: a.IPProto,
		DstPort: a.DstPort,
	}
	if a.SrcIP.IsValid() {
		aux.SrcIP = &a.SrcIP
	}
	if a.DstIP.IsValid() {
		aux.DstIP = &a.DstIP
	}
	return json.Marshal(aux)
}

// String prints all result attributes
func (a Attributes) String() string {
	return fmt.Sprintf("sip=%s dip=%s proto=%d dport=%d",
		a.SrcIP.String(),
		a.DstIP.String(),
		a.IPProto,
		a.DstPort,
	)
}

// Less returns wether the set of attributes a sorts before a2
func (a Attributes) Less(a2 Attributes) bool {
	if a.SrcIP != a2.SrcIP {
		return a.SrcIP.Less(a2.SrcIP)
	}
	if a.DstIP != a2.DstIP {
		return a.DstIP.Less(a2.DstIP)
	}
	if a.IPProto != a2.IPProto {
		return a.IPProto < a2.IPProto
	}
	return a.DstPort < a2.DstPort
}

// Rows is a list of results
type Rows []Row

// MergeableAttributes bundles all fields of a Result by which aggregation/merging is possible
type MergeableAttributes struct {
	Labels
	Attributes
}

// RowsMap is an aggregated representation of a Rows list
type RowsMap map[MergeableAttributes]types.Counters

// MergeRows aggregates Rows by use of the RowsMap rm, which is modified
// in the process
func (rm RowsMap) MergeRows(r Rows) (merged int) {
	for _, res := range r {
		counters, exists := rm[MergeableAttributes{res.Labels, res.Attributes}]
		if exists {
			rm[MergeableAttributes{res.Labels, res.Attributes}] = counters.Add(res.Counters)
			merged++
		} else {
			rm[MergeableAttributes{res.Labels, res.Attributes}] = res.Counters
		}
	}
	return
}

// MergeRowsMap aggregates all results of om and stores them in rm
func (rm RowsMap) MergeRowsMap(om RowsMap) (merged int) {
	for oma, oc := range om {
		counters, exists := rm[oma]
		if exists {
			rm[oma] = counters.Add(oc)
			merged++
		} else {
			rm[oma] = oc
		}
	}
	return
}

// ToRowsSorted uses the available sorting functions for Rows to produce
// a sorted Rows list from rm
func (rm RowsMap) ToRowsSorted(order by) Rows {
	r := rm.ToRows()
	order.Sort(r)
	return r
}

// ToRows produces a flat list of Rows from rm. Due to randomized map access,
// this list will _not_ be in any particular order. Use ToRowsSorted if you rely
// on order instead
func (rm RowsMap) ToRows() Rows {
	var r = make(Rows, len(rm))
	if len(rm) == 0 {
		return r
	}
	i := 0
	for ma, c := range rm {
		r[i] = Row{
			Labels:     ma.Labels,
			Attributes: ma.Attributes,
			Counters:   c,
		}
		i++
	}
	return r
}
