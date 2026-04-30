// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package vsphere

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"

	"github.com/lpasquali/yage/internal/config"
)

// vsphereSession bundles the govmomi client with its context so
// callers can use ctx for subsequent API calls without threading it
// back through separately. Close must be called when done.
type vsphereSession struct {
	client *govmomi.Client
	ctx    context.Context
	cancel context.CancelFunc
}

// Close logs out and cancels the context. Safe to call multiple times.
func (s *vsphereSession) Close() {
	if s.client != nil {
		_ = s.client.Logout(s.ctx)
	}
	if s.cancel != nil {
		s.cancel()
	}
}

// vsphereConnect opens an authenticated govmomi session for the
// vCenter configured in cfg.Providers.Vsphere. The caller must call
// sess.Close() when done (typically deferred).
//
// TLS behaviour:
//   - TLSThumbprint == "" → insecure=true (self-signed cert accepted
//     without validation; suitable for dev/lab vCenter).
//   - TLSThumbprint != "" → insecure=false with the thumbprint pinned
//     on the SOAP client so the TLS handshake is validated against the
//     known fingerprint rather than trusting all certs.
func vsphereConnect(cfg *config.Config) (*vsphereSession, error) {
	vs := cfg.Providers.Vsphere
	if vs.Server == "" || vs.Username == "" || vs.Password == "" {
		return nil, fmt.Errorf("vsphere: Server, Username and Password must all be set")
	}

	u, err := url.Parse("https://" + vs.Server + "/sdk")
	if err != nil {
		return nil, fmt.Errorf("vsphere: parse vCenter URL: %w", err)
	}
	u.User = url.UserPassword(vs.Username, vs.Password)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	insecure := vs.TLSThumbprint == ""
	soapClient := soap.NewClient(u, insecure)
	if vs.TLSThumbprint != "" {
		// Pin the certificate thumbprint so govmomi's custom DialTLS
		// hook validates the server cert by fingerprint instead of the
		// system CA chain (common with self-signed vCenter certs that
		// have a known thumbprint).
		soapClient.SetThumbprint(vs.Server, vs.TLSThumbprint)
	}

	vimClient, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("vsphere: vim25 connect to %s: %w", vs.Server, err)
	}

	c := &govmomi.Client{
		Client:         vimClient,
		SessionManager: session.NewManager(vimClient),
	}
	if err := c.Login(ctx, u.User); err != nil {
		cancel()
		return nil, fmt.Errorf("vsphere: login to %s: %w", vs.Server, err)
	}

	return &vsphereSession{client: c, ctx: ctx, cancel: cancel}, nil
}
