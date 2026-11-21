package main

import (
	"testing"

	"github.com/shogun-kub/termit/internal/proto"
	"github.com/shogun-kub/termit/internal/uv/config"
)

// A node that serves only the TLS profile says nothing about transports. The field is what the server
// needs to render a second link, and an older server decodes the sync body strictly — sending the default
// would turn one extra link into a node that cannot sync at all until its server is upgraded too.
func TestTheSyncReportsOnlyTheTransportsTheTLSProfileCannot(t *testing.T) {
	t.Parallel()
	app, _, _ := newTestApp(t)

	plain := config.Default()
	if got := app.newLoop(plain).Remote.Transports; got != nil {
		t.Errorf("a vless-only node reports %+v, want nothing", got)
	}

	quic := config.Default()
	quic.Hysteria = &config.Hysteria{}
	kinds := app.newLoop(quic).Remote.Transports
	if len(kinds) != 2 || kinds[0].Kind != proto.KindVLESS || kinds[1].Kind != proto.KindHysteria2 {
		t.Errorf("a node with the transport reports %+v, want vless and hysteria2", kinds)
	}
}
