package keys

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"

	sdkerrors "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/client/flags"
	cryptokeyring "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
)

type bechKeyOutFn func(k *cryptokeyring.Record) (KeyOutput, error)

func printKeyringRecord(w io.Writer, k *cryptokeyring.Record, bechKeyOut bechKeyOutFn, output string) error {
	ko, err := bechKeyOut(k)
	if err != nil {
		return err
	}

	switch output {
	case flags.OutputFormatText:
		if err := printTextRecords(w, []KeyOutput{ko}); err != nil {
			return err
		}

	case flags.OutputFormatJSON:
		out, err := json.Marshal(ko)
		if err != nil {
			return err
		}

		if _, err := fmt.Fprintln(w, string(out)); err != nil {
			return err
		}
	}

	return nil
}

func printKeyringRecords(w io.Writer, records []*cryptokeyring.Record, output string) error {
	kos, err := MkAccKeysOutput(records)
	if err != nil {
		return err
	}

	switch output {
	case flags.OutputFormatText:
		if err := printTextRecords(w, kos); err != nil {
			return err
		}

	case flags.OutputFormatJSON:
		out, err := json.Marshal(kos)
		if err != nil {
			return err
		}

		if _, err := fmt.Fprintf(w, "%s", out); err != nil {
			return err
		}
	}

	return nil
}

func printTextRecords(w io.Writer, kos []KeyOutput) error {
	out, err := yaml.Marshal(&kos)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w, string(out)); err != nil {
		return err
	}

	return nil
}

func SafeCreateED25519ValidatorKey(validatorKeyBase64 string) (cryptotypes.PubKey, error) {
	if validatorKeyBase64 == "" {
		return nil, sdkerrors.Wrap(errors.ErrInvalidPubKey, "validator key cannot be empty")
	}

	pubKeyBytes, err := base64.StdEncoding.DecodeString(validatorKeyBase64)
	if err != nil {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey, "failed to decode validator key: %v", err)
	}

	// Check size - ED25519 keys must be exactly 32 bytes (this is the core issue)
	if len(pubKeyBytes) != 32 {
		return nil, sdkerrors.Wrapf(errors.ErrInvalidPubKey,
			"ED25519 validator key must be exactly 32 bytes, got %d bytes", len(pubKeyBytes))
	}

	pubKey := &ed25519.PubKey{Key: pubKeyBytes}

	// Test that the key works - catch any panics from Address() call
	defer func() {
		if r := recover(); r != nil {
			err = sdkerrors.Wrapf(errors.ErrInvalidPubKey, "invalid ED25519 key format: %v", r)
		}
	}()

	_ = pubKey.Address() // This is where the panic occurs with invalid keys

	return pubKey, nil
}
