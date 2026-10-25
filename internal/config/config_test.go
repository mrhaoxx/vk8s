package config

import (
	"strings"
	"testing"
	"time"
)

func TestBookingRetentionDefault(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if c.Booking.Retention.Duration != 168*time.Hour {
		t.Fatalf("got %v", c.Booking.Retention.Duration)
	}
}

func TestAuditDefaultsAndValidation(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if c.Audit.RetentionDays != 365 || c.Audit.QueueSize != 50000 || c.Audit.BatchSize != 2000 || c.Audit.BatchInterval.Duration != 2*time.Second || c.Audit.ClickHouse.Database != "vk8s_audit" {
		t.Fatalf("defaults = %+v", c.Audit)
	}
	c.APIDomain, c.AppsDomain, c.Image, c.Auth.PublicURL = "a", "b", "img", "https://x"
	c.APIPort = 443
	c.Defaults.PodSecurity = "privileged"
	if err := c.Validate(); err != nil {
		t.Fatalf("baseline config must validate: %v", err)
	}
	c.Audit.Enabled = true
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "clickhouse.url") {
		t.Fatalf("enabled without url must fail, got %v", err)
	}
	c.Audit.ClickHouse.URL = "http://ch:8123"
	if err := c.Validate(); err != nil {
		t.Fatalf("enabled with url must validate: %v", err)
	}
}
