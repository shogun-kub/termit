package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/shogun-kub/termit/internal/mn/config"
	"github.com/shogun-kub/termit/internal/mn/store"
)

// masterKeyEnv lets a deployment take the master key from the environment instead of a file: systemd-creds
// hands it over that way, and a key that was never written to disk cannot be copied from it
// (docs/IMPLEMENTATION_PLAN.md §5.2.1 decision 8).
const masterKeyEnv = "TERMIT_MN_MASTER"

// Where resolveMasterKey found the key. `termit-mn status` prints it, because "this server holds the key"
// and "it holds it on disk" are different facts about a machine that may have to take over.
const (
	masterKeyFromEnv  = "environment"
	masterKeyFromFile = "file"
)

// resolveMasterKey reads the key the way decision 8 says: the environment first, the file second. A standby
// takes the key from disk (it must survive a reboot with nobody there); a replica that is not expected to
// take over can be given nothing, and then it cannot derive a pair or render a subscription — which is the
// point of not keeping the key on a host that has no business holding it.
func resolveMasterKey(cfg *config.Config) (key []byte, source string, err error) {
	if fromEnv := strings.TrimSpace(os.Getenv(masterKeyEnv)); fromEnv != "" {
		decoded, decodeErr := hex.DecodeString(fromEnv)
		if decodeErr != nil {
			return nil, "", fmt.Errorf("%s is not hex: %w", masterKeyEnv, decodeErr)
		}
		if len(decoded) < 32 {
			return nil, "", fmt.Errorf("%s holds %d bytes, want at least 32", masterKeyEnv, len(decoded))
		}
		return decoded, masterKeyFromEnv, nil
	}
	fromFile, err := store.ReadMasterKey(cfg.MasterKeyFile)
	if err != nil {
		return nil, "", err
	}
	return fromFile, masterKeyFromFile, nil
}
