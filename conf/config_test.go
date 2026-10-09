package conf

import (
	"encoding/json"
	"testing"
)

func TestShowAdminAddrEnabled(t *testing.T) {
	tests := []struct {
		name string
		json string
		want bool
	}{
		{"缺省(nil)=展示", `{}`, true},
		{"显式 true=展示", `{"show_admin_addr": true}`, true},
		{"显式 false=隐藏", `{"show_admin_addr": false}`, false},
		{"null=展示", `{"show_admin_addr": null}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ui UIConfig
			if err := json.Unmarshal([]byte(tt.json), &ui); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := ui.ShowAdminAddrEnabled(); got != tt.want {
				t.Errorf("ShowAdminAddrEnabled(%s) = %v, want %v", tt.json, got, tt.want)
			}
		})
	}
}
