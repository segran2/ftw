package state

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

func DriverSecretKey(owner, key string) string {
	return "driver_secret:" + owner + ":" + key
}

func DriverSecretLegacyHashKey(owner, key string) string {
	return "driver_secret_legacy_hash:" + owner + ":" + key
}

func DriverSecretValueHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func driverSecretAliases(document []byte) (map[string]string, error) {
	var saved struct {
		Config struct {
			Drivers []struct {
				Name  string `json:"name"`
				Owner string `json:"credential_owner"`
			} `json:"drivers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(document, &saved); err != nil {
		return nil, err
	}
	aliases := map[string]string{}
	for _, d := range saved.Config.Drivers {
		if d.Owner != "" && d.Owner != d.Name {
			aliases[d.Owner] = d.Name
		}
	}
	return aliases, nil
}

// SaveDriverSecret keeps the current name-keyed copy usable by an older Core
// after rollback. Read the binding and write both copies in one transaction so
// a concurrent rename cannot give a rotation to a different driver's name.
func (s *Store) SaveDriverSecret(owner, key, value string) error {
	return s.durableConfigWrite(func(tx *sql.Tx) error {
		values := map[string]string{DriverSecretKey(owner, key): value}
		var raw string
		err := tx.QueryRow(`SELECT value FROM config WHERE key = ?`, configurationKey).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			c, err := decodeConfiguration(raw)
			if err != nil {
				return err
			}
			aliases, err := driverSecretAliases(c.Document)
			if err != nil {
				return err
			}
			if name, ok := aliases[owner]; ok {
				values[DriverSecretKey(name, key)] = value
				values[DriverSecretLegacyHashKey(owner, key)] = DriverSecretValueHash(value)
			}
		}
		return saveConfigValues(tx, values)
	})
}

// Settings saves mirror the latest rotation inside their own transaction,
// after applying explicit credentials. A token rotated during the settings
// read cannot be replaced by an older snapshot.
func mirrorDriverSecrets(tx *sql.Tx, document []byte) error {
	aliases, err := driverSecretAliases(document)
	if err != nil {
		return err
	}
	for owner, name := range aliases {
		prefix := DriverSecretKey(owner, "")
		rows, err := tx.Query(`SELECT key, value FROM config WHERE key LIKE ? ESCAPE '\'`, escapeConfigPrefix(prefix)+"%")
		if err != nil {
			return err
		}
		values := map[string]string{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return err
			}
			secretKey := key[len(prefix):]
			values[DriverSecretKey(name, secretKey)] = value
			values[DriverSecretLegacyHashKey(owner, secretKey)] = DriverSecretValueHash(value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if err := saveConfigValues(tx, values); err != nil {
			return err
		}
	}
	return nil
}
