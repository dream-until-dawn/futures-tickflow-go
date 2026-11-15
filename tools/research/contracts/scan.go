package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 目录字段独立于交易时段，避免把最后一天的快照当作历史模板。
type contract struct {
	SourceKey string      `json:"source_key"`
	Canonical string      `json:"canonical"`
	Exchange  string      `json:"exchange"`
	Product   string      `json:"product"`
	Year      int         `json:"delivery_year"`
	Month     int         `json:"delivery_month"`
	Expiry    json.Number `json:"expiry_seconds"`
}

type inventory struct {
	ByClass   map[string]int `json:"by_class"`
	ByMarket  map[string]int `json:"by_market"`
	Contracts []contract     `json:"contracts"`
	Conflicts []contract     `json:"product_identity_conflicts"`
}

var futureKey = regexp.MustCompile(`^([A-Za-z]+)([0-9]{3,4})$`)
var markets = map[string]bool{"SHFE": true, "INE": true, "DCE": true, "CZCE": true, "GFEX": true, "CFFEX": true}

func scan(r io.Reader) (inventory, error) {
	return scanWithPolicy(r, false)
}

// 显式离线恢复才隔离品种别名冲突；原严格取证默认仍报错，不改旧收据。
func scanWithPolicy(r io.Reader, quarantine bool) (inventory, error) {
	result := inventory{ByClass: map[string]int{}, ByMarket: map[string]int{}, Contracts: []contract{}, Conflicts: []contract{}}
	d := json.NewDecoder(r)
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return inventory{}, fmt.Errorf("catalog_start: %v", err)
	}
	seen := map[string]bool{}
	canonicalSeen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return inventory{}, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return inventory{}, fmt.Errorf("catalog_duplicate_key: %v", token)
		}
		seen[key] = true
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return inventory{}, fmt.Errorf("catalog_entry %s: %w", key, err)
		}
		var head struct {
			Class string `json:"class"`
		}
		if err = json.Unmarshal(raw, &head); err != nil || head.Class == "" {
			return inventory{}, fmt.Errorf("catalog_class: %s", key)
		}
		result.ByClass[head.Class]++
		if head.Class != "FUTURE" {
			continue
		}
		var entry struct {
			Class    string      `json:"class"`
			Exchange string      `json:"exchange_id"`
			Product  string      `json:"product_id"`
			Year     int         `json:"delivery_year"`
			Month    int         `json:"delivery_month"`
			Expiry   json.Number `json:"expire_datetime"`
		}
		if err = json.Unmarshal(raw, &entry); err != nil {
			return inventory{}, fmt.Errorf("catalog_entry %s: %w", key, err)
		}
		result.ByMarket[entry.Exchange]++
		if !markets[entry.Exchange] {
			continue
		}
		parts := strings.Split(key, ".")
		if len(parts) != 2 || parts[0] != entry.Exchange || entry.Year < 1900 || entry.Year > 2200 || entry.Month < 1 || entry.Month > 12 {
			return inventory{}, fmt.Errorf("catalog_identity: %s", key)
		}
		match := futureKey.FindStringSubmatch(parts[1])
		if match == nil {
			return inventory{}, fmt.Errorf("catalog_product: %s", key)
		}
		yearDigits := 2
		if len(match[2]) == 3 {
			yearDigits = 1
		}
		yearMod := 100
		if yearDigits == 1 {
			yearMod = 10
		}
		if match[2] != fmt.Sprintf("%0*d%02d", yearDigits, entry.Year%yearMod, entry.Month) {
			return inventory{}, fmt.Errorf("catalog_delivery: %s", key)
		}
		expiry, err := strconv.ParseFloat(string(entry.Expiry), 64)
		if err != nil || !(expiry > 0 && expiry < 1e11) {
			return inventory{}, fmt.Errorf("catalog_expiry: %s", key)
		}
		canonical := fmt.Sprintf("%s.%s%02d%02d", entry.Exchange, entry.Product, entry.Year%100, entry.Month)
		if match[1] != entry.Product {
			if !quarantine {
				return inventory{}, fmt.Errorf("catalog_product: %s", key)
			}
			result.Conflicts = append(result.Conflicts, contract{key, canonical, entry.Exchange, entry.Product, entry.Year, entry.Month, entry.Expiry})
			continue
		}
		if canonicalSeen[canonical] {
			return inventory{}, fmt.Errorf("catalog_duplicate_identity: %s", canonical)
		}
		canonicalSeen[canonical] = true
		result.Contracts = append(result.Contracts, contract{key, canonical, entry.Exchange, entry.Product, entry.Year, entry.Month, entry.Expiry})
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return inventory{}, fmt.Errorf("catalog_end: %v", err)
	}
	var tail json.RawMessage
	if err := d.Decode(&tail); err != io.EOF {
		return inventory{}, fmt.Errorf("catalog_tail: %v", err)
	}
	if len(result.Contracts) == 0 {
		return inventory{}, fmt.Errorf("catalog_no_futures")
	}
	sort.Slice(result.Contracts, func(i, j int) bool { return result.Contracts[i].SourceKey < result.Contracts[j].SourceKey })
	return result, nil
}
