package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	"github.com/df-mc/go-nethernet"
	"github.com/sandertv/gophertunnel/minecraft"
)

// loadIdentityKey returns the P-384 key stored at path, creating it on first use. NetherNet server
// identities are signed with ES384, hence P-384.
func loadIdentityKey(path string) (*ecdsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(b)
		if block == nil {
			return nil, fmt.Errorf("%s: no PEM block found", path)
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, fmt.Errorf("save identity key: %w", err)
	}
	return key, nil
}

// issueIdentity returns a function that issues short-lived server identities from the stored key.
func issueIdentity(key *ecdsa.PrivateKey) func(context.Context) (*nethernet.Identity, error) {
	return func(context.Context) (*nethernet.Identity, error) {
		return nethernet.GenerateServerIdentity(key, "self")
	}
}

// status is the server list entry shown to players.
type status struct{ motd string }

func (s status) ServerStatus(players, max int) minecraft.ServerStatus {
	return minecraft.ServerStatus{ServerName: s.motd, PlayerCount: players, MaxPlayers: max}
}
