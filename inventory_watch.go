package main

import (
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"openflux/transport/control"
	"openflux/transport/manager"
	"openflux/utils"
)

var (
	channelInventoryPath = flag.String("channel-inventory", "",
		"Client: normalized MultiBond channel CSV to hot-reload at runtime")
	channelInventoryInterval = flag.Duration("channel-inventory-interval", 5*time.Second,
		"Client: polling interval for --channel-inventory")
)

type channelInventoryRow struct {
	Name     string
	Type     string
	Priority int
	URL      string
	Dial     string
	Listen   string
	Token    string
	UID      string
}

func (r channelInventoryRow) fingerprint() string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		r.Name, r.Type, r.Priority, r.URL, r.Dial, r.Listen, r.Token, r.UID)
}

func (r channelInventoryRow) transportConfig(exit bool) *control.TransportConfig {
	params := map[string]interface{}{
		"priority": float64(r.Priority),
	}
	switch r.Type {
	case "direct":
		params["is_exit"] = exit
		if exit {
			params["listen"] = r.Listen
		} else {
			params["dial"] = r.Dial
		}
	case "oneme":
		params["token"] = r.Token
		params["uid"] = r.UID
		params["exit"] = exit
	case "cupsonline":
		params["is_exit"] = exit
	}
	return &control.TransportConfig{
		Name:   r.Name,
		Type:   r.Type,
		URL:    r.URL,
		Params: params,
	}
}

func parseChannelInventory(data []byte) ([]channelInventoryRow, error) {
	rd := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff")))
	rd.FieldsPerRecord = -1
	rd.TrimLeadingSpace = true

	records, err := rd.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("inventory CSV: %w", err)
	}
	if len(records) == 0 {
		return nil, errors.New("inventory CSV is empty")
	}

	index := make(map[string]int, len(records[0]))
	for i, raw := range records[0] {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name != "" {
			index[name] = i
		}
	}
	for _, required := range []string{"name", "type"} {
		if _, ok := index[required]; !ok {
			return nil, fmt.Errorf("inventory CSV missing required column %q", required)
		}
	}

	field := func(row []string, name string) string {
		i, ok := index[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	disabled := map[string]bool{
		"0": true, "false": true, "no": true, "off": true, "disabled": true,
	}
	seen := make(map[string]bool)
	out := make([]channelInventoryRow, 0, len(records)-1)
	for line, row := range records[1:] {
		name := field(row, "name")
		if name == "" {
			continue
		}
		enabled := strings.ToLower(field(row, "enabled"))
		if disabled[enabled] {
			continue
		}
		if seen[name] {
			return nil, fmt.Errorf("inventory CSV line %d: duplicate channel %q", line+2, name)
		}
		seen[name] = true

		typ := strings.ToLower(field(row, "type"))
		if typ == "" {
			return nil, fmt.Errorf("inventory CSV line %d: channel %q has empty type", line+2, name)
		}
		priority := 50
		if raw := field(row, "priority"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("inventory CSV line %d: bad priority %q", line+2, raw)
			}
			priority = v
		}

		item := channelInventoryRow{
			Name:     name,
			Type:     typ,
			Priority: priority,
			URL:      field(row, "url"),
			Dial:     field(row, "dial"),
			Listen:   field(row, "listen"),
			Token:    field(row, "token"),
			UID:      field(row, "uid"),
		}
		if item.Type == "direct" {
			if item.Dial == "" || item.Listen == "" {
				return nil, fmt.Errorf("inventory CSV line %d: direct channel %q needs both dial and listen", line+2, name)
			}
		}
		out = append(out, item)
	}
	if len(out) > 256 {
		return nil, fmt.Errorf("inventory has %d enabled channels; maximum is 256", len(out))
	}
	return out, nil
}

// channelInventoryReconciler owns only channels present in the inventory.
// Bootstrap transports that are not listed are deliberately left alone so
// the control session always has an out-of-band anchor for hot reloads.
type channelInventoryReconciler struct {
	manager *manager.Manager
	managed map[string]channelInventoryRow
}

func newChannelInventoryReconciler(m *manager.Manager) *channelInventoryReconciler {
	return &channelInventoryReconciler{
		manager: m,
		managed: make(map[string]channelInventoryRow),
	}
}

func (r *channelInventoryReconciler) apply(rows []channelInventoryRow) error {
	desired := make(map[string]channelInventoryRow, len(rows))
	for _, row := range rows {
		desired[row.Name] = row
	}

	existing := make(map[string]bool)
	for _, name := range r.manager.Transports() {
		existing[name] = true
	}

	// Adopt bootstrap transports that also appear in the first inventory.
	// They become inventory-managed from this point forward.
	for _, row := range rows {
		if existing[row.Name] {
			if _, ok := r.managed[row.Name]; !ok {
				r.managed[row.Name] = row
				utils.Debugf("[INVENTORY] adopted existing channel %q", row.Name)
			}
		}
	}

	var errs []error

	// Remove rows disabled/deleted from the inventory.
	for name, old := range r.managed {
		if _, ok := desired[name]; ok {
			continue
		}
		if err := r.remove(old); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", name, err))
			continue
		}
		delete(r.managed, name)
		delete(existing, name)
	}

	// Replace changed rows. Priority changes are replacements too because
	// Session ordering is immutable for an attached transport.
	for _, next := range rows {
		old, ok := r.managed[next.Name]
		if !ok || old.fingerprint() == next.fingerprint() {
			continue
		}
		if err := r.replace(old, next); err != nil {
			errs = append(errs, fmt.Errorf("replace %s: %w", next.Name, err))
			continue
		}
		r.managed[next.Name] = next
		existing[next.Name] = true
	}

	// Add newly enabled rows.
	for _, row := range rows {
		if existing[row.Name] {
			continue
		}
		if err := r.add(row); err != nil {
			errs = append(errs, fmt.Errorf("add %s: %w", row.Name, err))
			continue
		}
		r.managed[row.Name] = row
		existing[row.Name] = true
	}

	return errors.Join(errs...)
}

func (r *channelInventoryReconciler) canRemove(name string) bool {
	live := r.manager.Session().LiveTransports()
	for _, n := range live {
		if n == name {
			return len(live) > 1
		}
	}
	return true
}

func (r *channelInventoryReconciler) sendPeer(sub control.Subtype, cfg *control.TransportConfig) error {
	body, err := cfg.Encode()
	if err != nil {
		return err
	}
	return r.manager.SendControl(sub, body)
}

func (r *channelInventoryReconciler) add(row channelInventoryRow) error {
	utils.Debugf("[INVENTORY] add %s type=%s priority=%d", row.Name, row.Type, row.Priority)
	if err := r.manager.StartTransport(row.transportConfig(false)); err != nil {
		return fmt.Errorf("local start: %w", err)
	}
	if err := r.sendPeer(control.SubtypeTransportStart, row.transportConfig(true)); err != nil {
		_ = r.manager.Remove(row.Name)
		return fmt.Errorf("peer start: %w", err)
	}
	return nil
}

func (r *channelInventoryReconciler) remove(row channelInventoryRow) error {
	if !r.canRemove(row.Name) {
		return errors.New("refusing to remove the last live carrier; keep a bootstrap/control anchor")
	}
	utils.Debugf("[INVENTORY] remove %s", row.Name)
	if err := r.sendPeer(control.SubtypeTransportStop, row.transportConfig(true)); err != nil {
		return fmt.Errorf("peer stop: %w", err)
	}
	if err := r.manager.Remove(row.Name); err != nil {
		return fmt.Errorf("local stop: %w", err)
	}
	return nil
}

func (r *channelInventoryReconciler) replace(old, next channelInventoryRow) error {
	if !r.canRemove(old.Name) {
		return errors.New("refusing to replace the last live carrier; bring another carrier up first")
	}
	utils.Debugf("[INVENTORY] replace %s type=%s->%s priority=%d->%d",
		old.Name, old.Type, next.Type, old.Priority, next.Priority)
	if err := r.remove(old); err != nil {
		return err
	}
	if err := r.add(next); err != nil {
		// Best-effort rollback to the last known-good row. Even if the peer
		// start fails, keeping the old local carrier lets the next poll retry.
		_ = r.add(old)
		return err
	}
	return nil
}

func startConfiguredChannelInventory(m *manager.Manager, role string) {
	if m == nil || *channelInventoryPath == "" {
		return
	}
	if role != roleClient {
		log.Printf("channel inventory ignored on %s: the client is the inventory controller", role)
		return
	}
	if *channelInventoryInterval <= 0 {
		log.Printf("channel inventory disabled: interval must be > 0")
		return
	}

	path := *channelInventoryPath
	interval := *channelInventoryInterval
	log.Printf("Channel inventory: %s (hot reload every %v)", path, interval)

	utils.SafeGo("channel-inventory", func() {
		reconciler := newChannelInventoryReconciler(m)
		var lastSuccess [32]byte
		haveSuccess := false
		lastErr := ""

		apply := func() {
			data, err := os.ReadFile(path)
			if err != nil {
				msg := err.Error()
				if msg != lastErr {
					log.Printf("channel inventory read: %v", err)
					lastErr = msg
				}
				return
			}
			sum := sha256.Sum256(data)
			if haveSuccess && sum == lastSuccess {
				return
			}
			rows, err := parseChannelInventory(data)
			if err == nil {
				err = reconciler.apply(rows)
			}
			if err != nil {
				msg := err.Error()
				if msg != lastErr {
					log.Printf("channel inventory apply: %v", err)
					lastErr = msg
				}
				return
			}
			lastSuccess = sum
			haveSuccess = true
			lastErr = ""
			log.Printf("Channel inventory applied: %d enabled channels", len(rows))
		}

		apply()
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for range tick.C {
			apply()
		}
	})
}
