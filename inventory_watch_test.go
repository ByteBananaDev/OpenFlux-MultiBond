package main

import (
	"testing"

	"openflux/transport/control"
)

type fakeInventoryManager struct {
	transports []string
	live       []string
	started    []*control.TransportConfig
	removed    []string
	sent       []control.Subtype
}

func (f *fakeInventoryManager) Transports() []string {
	return append([]string(nil), f.transports...)
}

func (f *fakeInventoryManager) LiveTransports() []string {
	return append([]string(nil), f.live...)
}

func (f *fakeInventoryManager) StartTransport(cfg *control.TransportConfig) error {
	f.started = append(f.started, cfg)
	f.transports = append(f.transports, cfg.Name)
	return nil
}

func (f *fakeInventoryManager) Remove(name string) error {
	f.removed = append(f.removed, name)
	out := f.transports[:0]
	for _, n := range f.transports {
		if n != name {
			out = append(out, n)
		}
	}
	f.transports = out
	return nil
}

func (f *fakeInventoryManager) SendControl(sub control.Subtype, payload []byte) error {
	f.sent = append(f.sent, sub)
	return nil
}

func TestParseChannelInventory(t *testing.T) {
	rows, err := parseChannelInventory([]byte(
		"name,type,priority,url,dial,listen,token,uid,enabled\n" +
			"volga-01,vyandex,70,https://example.invalid/doc,,,,,1\n" +
			"direct-01,direct,100,,203.0.113.10:18445,0.0.0.0:18445,,,true\n" +
			"disabled,boards,50,https://example.invalid/off,,,,,0\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("enabled rows=%d, want 2", len(rows))
	}
	if rows[0].Name != "volga-01" || rows[0].Priority != 70 {
		t.Fatalf("unexpected first row: %#v", rows[0])
	}

	client := rows[1].transportConfig(false)
	exit := rows[1].transportConfig(true)
	if client.Params["dial"] != "203.0.113.10:18445" || client.Params["is_exit"] != false {
		t.Fatalf("bad client direct config: %#v", client.Params)
	}
	if exit.Params["listen"] != "0.0.0.0:18445" || exit.Params["is_exit"] != true {
		t.Fatalf("bad exit direct config: %#v", exit.Params)
	}
}

func TestParseChannelInventoryRejectsBadDirectAndDuplicates(t *testing.T) {
	if _, err := parseChannelInventory([]byte(
		"name,type,dial,listen\n" +
			"d,direct,203.0.113.1:1,\n",
	)); err == nil {
		t.Fatal("direct row without listen should fail")
	}

	if _, err := parseChannelInventory([]byte(
		"name,type\n" +
			"a,yandex\n" +
			"a,boards\n",
	)); err == nil {
		t.Fatal("duplicate names should fail")
	}
}

func TestInventoryReconcileAddReplaceRemoveKeepsBootstrap(t *testing.T) {
	fm := &fakeInventoryManager{
		transports: []string{"bootstrap"},
		live:       []string{"bootstrap"},
	}
	r := newChannelInventoryReconciler(fm)

	first := channelInventoryRow{
		Name: "volga-01", Type: "vyandex", Priority: 50,
		URL: "https://example.invalid/one",
	}
	if err := r.apply([]channelInventoryRow{first}); err != nil {
		t.Fatal(err)
	}
	if len(fm.started) != 1 || fm.started[0].Name != "volga-01" {
		t.Fatalf("start calls=%v", fm.started)
	}
	if len(fm.sent) != 1 || fm.sent[0] != control.SubtypeTransportStart {
		t.Fatalf("control calls=%v", fm.sent)
	}

	// Keep the bootstrap carrier live while replacing the managed row.
	fm.live = []string{"bootstrap", "volga-01"}
	next := first
	next.URL = "https://example.invalid/two"
	next.Priority = 80
	if err := r.apply([]channelInventoryRow{next}); err != nil {
		t.Fatal(err)
	}
	if len(fm.removed) != 1 || fm.removed[0] != "volga-01" {
		t.Fatalf("replace did not remove old row: %v", fm.removed)
	}
	if len(fm.started) != 2 || fm.started[1].Params["priority"] != float64(80) {
		t.Fatalf("replace did not start new row: %#v", fm.started)
	}

	fm.live = []string{"bootstrap", "volga-01"}
	if err := r.apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(fm.transports) != 1 || fm.transports[0] != "bootstrap" {
		t.Fatalf("bootstrap should remain unmanaged: %v", fm.transports)
	}
}

func TestInventoryRefusesToRemoveLastLiveCarrier(t *testing.T) {
	fm := &fakeInventoryManager{
		transports: []string{"only"},
		live:       []string{"only"},
	}
	r := newChannelInventoryReconciler(fm)
	row := channelInventoryRow{Name: "only", Type: "yandex", URL: "https://example.invalid/doc"}

	// First snapshot adopts the already-running channel.
	if err := r.apply([]channelInventoryRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := r.apply(nil); err == nil {
		t.Fatal("expected last-live-carrier protection")
	}
	if len(fm.transports) != 1 || fm.transports[0] != "only" {
		t.Fatalf("last live carrier was removed: %v", fm.transports)
	}
}
