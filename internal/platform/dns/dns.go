package dns

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/httpx"
	"github.com/marstack-labs/marstack-cloud/internal/store"
)

const Suffix = "internal"

type InstanceRef struct {
	ID        string
	Name      string
	NetworkID string
}

type InstanceSource interface {
	AllInstances(ctx context.Context) ([]InstanceRef, error)
}

type AddressSource interface {
	AllAddresses(ctx context.Context) (map[string]string, error)
	NetworkNames(ctx context.Context) (map[string]string, error)
}

type Record struct {
	FQDN string
	IP   string
}

type Module struct {
	log       *slog.Logger
	instances InstanceSource
	addresses AddressSource
}

func New(log *slog.Logger, instances InstanceSource, addresses AddressSource) *Module {
	return &Module{log: log, instances: instances, addresses: addresses}
}

func (m *Module) Name() string {
	return "dns"
}

func (m *Module) Migrations() []store.Migration {
	return nil
}

func (m *Module) Routes(mux *http.ServeMux) {
	mux.Handle("GET /v1/dns/records", httpx.Wrap(m.log, m.handleRecords))
}

type recordResponse struct {
	FQDN string `json:"fqdn"`
	IP   string `json:"ip"`
}

type recordsResponse struct {
	Records []recordResponse `json:"records"`
}

func (m *Module) handleRecords(w http.ResponseWriter, r *http.Request) error {
	records, err := m.Records(r.Context())
	if err != nil {
		return err
	}

	body := recordsResponse{Records: make([]recordResponse, 0, len(records))}
	for _, record := range records {
		body.Records = append(body.Records, recordResponse{FQDN: record.FQDN, IP: record.IP})
	}

	httpx.Write(w, http.StatusOK, body)
	return nil
}

func (m *Module) Records(ctx context.Context) ([]Record, error) {
	instances, err := m.instances.AllInstances(ctx)
	if err != nil {
		return nil, err
	}

	addresses, err := m.addresses.AllAddresses(ctx)
	if err != nil {
		return nil, err
	}

	networks, err := m.addresses.NetworkNames(ctx)
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0, len(instances))
	for _, in := range instances {
		ip, addressed := addresses[in.ID]
		if !addressed {
			continue
		}
		zone, named := networks[in.NetworkID]
		if !named {
			continue
		}
		records = append(records, Record{
			FQDN: strings.Join([]string{in.Name, zone, Suffix}, "."),
			IP:   ip,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].FQDN < records[j].FQDN })
	return records, nil
}
