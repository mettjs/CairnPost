package config

import (
	"fmt"
	"strings"
	"time"
)

// Inbound is how emails that come are taken and kept.
type Inbound struct {
	// Secret is CAIRNPOST_INBOUND_SECRET: what whoever hands emails on
	// signs with. Empty, the default, takes none.
	Secret string
	// Retention is CAIRNPOST_INBOUND_RETENTION: how long an email that
	// came is kept. Default 7d.
	Retention time.Duration
	// MaxBytes is CAIRNPOST_INBOUND_MAX_BYTES: how long one may be.
	// Default 25 MiB, the most the mail provider hands on.
	MaxBytes int64
	// RoomBytes is CAIRNPOST_INBOUND_ROOM_BYTES: how many bytes the
	// emails kept may be in all. Default 1 GiB.
	RoomBytes int64
}

// minInboundSecret is the shortest secret taken: a short one can be
// guessed.
const minInboundSecret = 32

func inbound(getenv func(string) string) (Inbound, error) {
	in := Inbound{Secret: strings.TrimSpace(getenv("CAIRNPOST_INBOUND_SECRET")), Retention: 7 * 24 * time.Hour}
	if in.Secret != "" && len(in.Secret) < minInboundSecret {
		return in, fmt.Errorf("config: CAIRNPOST_INBOUND_SECRET must be at least %d characters; make one with: openssl rand -hex 32", minInboundSecret)
	}
	if v := strings.ToLower(strings.TrimSpace(getenv("CAIRNPOST_INBOUND_RETENTION"))); v != "" {
		keep, ok := span(v)
		if !ok || keep < time.Hour || keep > 30*24*time.Hour {
			return in, fmt.Errorf("config: CAIRNPOST_INBOUND_RETENTION must be a time such as 7d or 48h, between 1h and 30d (got %q)", v)
		}
		in.Retention = keep
	}
	var err error
	if in.MaxBytes, err = number(getenv, "CAIRNPOST_INBOUND_MAX_BYTES", 25<<20); err != nil {
		return in, err
	}
	if in.MaxBytes > 64<<20 {
		return in, fmt.Errorf("config: CAIRNPOST_INBOUND_MAX_BYTES must be at most 67108864 (got %d)", in.MaxBytes)
	}
	if in.RoomBytes, err = number(getenv, "CAIRNPOST_INBOUND_ROOM_BYTES", 1<<30); err != nil {
		return in, err
	}
	if in.RoomBytes < in.MaxBytes {
		return in, fmt.Errorf("config: CAIRNPOST_INBOUND_ROOM_BYTES must be at least CAIRNPOST_INBOUND_MAX_BYTES, %d (got %d)", in.MaxBytes, in.RoomBytes)
	}
	return in, nil
}
