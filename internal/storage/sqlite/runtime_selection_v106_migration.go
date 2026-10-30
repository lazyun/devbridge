package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Early development builds recorded version 104 before its prepare fence and
// original no-Run command identity were added. Reconcile only those recognized
// shapes; never infer an authorization from historical business completion.
// Version 105 invokes this first because SQLite validates unrelated triggers
// during its table rename, and one early 104 trigger names a nonexistent column.
func migrateBackendPlatformV105(ctx context.Context, tx *sql.Tx) error {
	if err := migrateRuntimeSelectionV106(ctx, tx); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, backendPlatformV105SQL)
	return err
}

type runtimeSelectionSchemaObject struct{ kind, name, statement string }

var runtimeSelectionDDLHeader = regexp.MustCompile(`(?m)^(CREATE|DROP) (TABLE|TRIGGER) ([a-zA-Z0-9_]+)`)

func runtimeSelectionV104Objects() []runtimeSelectionSchemaObject {
	text := runtimeSelectionAdoptionV104SQL
	locations := runtimeSelectionDDLHeader.FindAllStringSubmatchIndex(text, -1)
	var out []runtimeSelectionSchemaObject
	for i, match := range locations {
		if text[match[2]:match[3]] != "CREATE" {
			continue
		}
		end := len(text)
		if i+1 < len(locations) {
			end = locations[i+1][0]
		}
		out = append(out, runtimeSelectionSchemaObject{kind: strings.ToLower(text[match[4]:match[5]]), name: text[match[6]:match[7]], statement: strings.TrimSpace(text[match[0]:end])})
	}
	return out
}

func runtimeSelectionSchemaText(text string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
}

func runtimeSelectionSchemaHash(text string) string {
	sum := sha256.Sum256([]byte(runtimeSelectionSchemaText(text)))
	return hex.EncodeToString(sum[:])
}

// Exact definitions from the two shipped-to-development 104 variants. Their
// source databases and genuine native qualification logs are retained in the
// migration regression evidence. A name alone is never sufficient to replace
// an unknown table or trigger.
var runtimeSelectionKnownEarlyV104 = map[string][]string{
	"formal_adoption_business":        {"d01dc6bfce0cd2b47f3ad22f227c72997f16d4fb640555d03d44ff50278fb26e"},
	"formal_adoption_business_source": {"8df630dbf985228e7187fb8afc1b867d2d97a6d7b854d63da22052f52637216b", "739532b89e4bc6100a87edee2721a41a3aea5ba44cc61fc44eae0fe245624793"},
	"no_run_successor_effect_owner":   {"793657e6c2a53b02cbdd204ba30a5f67e69b8febd825912d34c21b46ba00fae6"},
}

func migrateRuntimeSelectionV106(ctx context.Context, tx *sql.Tx) error {
	objects := runtimeSelectionV104Objects()
	if len(objects) != 20 {
		return fmt.Errorf("v106: unexpected canonical v104 object inventory (%d)", len(objects))
	}
	existing := map[string]string{}
	missing := map[string]bool{}
	replace := map[string]bool{}
	for _, object := range objects {
		var kind, text string
		err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, object.name).Scan(&kind, &text)
		if err == sql.ErrNoRows {
			missing[object.name] = true
			continue
		}
		if err != nil {
			return err
		}
		if kind != object.kind {
			return fmt.Errorf("v106: unsupported %s object type %q", object.name, kind)
		}
		existing[object.name] = text
		if runtimeSelectionSchemaText(text) == runtimeSelectionSchemaText(object.statement) {
			continue
		}
		hash, known := runtimeSelectionSchemaHash(text), false
		for _, approved := range runtimeSelectionKnownEarlyV104[object.name] {
			known = known || hash == approved
		}
		if !known {
			return fmt.Errorf("v106: unsupported schema for %s (sha256=%s); database was not repaired", object.name, hash)
		}
		replace[object.name] = true
	}
	// Rebuilding an empty recognized table must not silently discard a custom
	// index or trigger. Automatic UNIQUE/PK indexes are owned by its table DDL.
	managed := map[string]bool{}
	for _, object := range objects {
		managed[object.name] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE tbl_name IN ('formal_adoption_fences','formal_adoption_business','formal_adoption_receipts','no_run_successor_command_identities') AND name NOT LIKE 'sqlite_autoindex_%'`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if !managed[name] {
			rows.Close()
			return fmt.Errorf("v106: unsupported extra v104 object %s; database was not repaired", name)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// A complete canonical schema is a no-op, including all existing archives.
	if len(missing) == 0 && len(replace) == 0 {
		return nil
	}
	allowedMissing := map[string]bool{
		"formal_adoption_fences": true, "formal_adoption_fence_source": true,
		"formal_adoption_fence_immutable": true, "formal_adoption_fence_durable": true,
		"formal_adoption_confirmation_completion": true,
		"no_run_successor_command_identities":     true, "no_run_successor_command_identity_source": true,
		"no_run_successor_command_identity_receipt": true, "no_run_successor_command_identity_immutable": true,
		"no_run_successor_command_identity_durable": true,
	}
	for name := range missing {
		if !allowedMissing[name] {
			return fmt.Errorf("v106: unsupported missing v104 object %s", name)
		}
	}
	// Only the observed pre-fence/pre-identity family is a supported old shape.
	// Partial manual deletion of current guards is not a migration source.
	if len(missing) != len(allowedMissing) || !replace["formal_adoption_business"] || !replace["formal_adoption_business_source"] || !replace["no_run_successor_effect_owner"] {
		return fmt.Errorf("v106: unsupported partial v104 schema; no historical authority was inferred")
	}
	for _, table := range []string{"formal_adoption_business", "formal_adoption_receipts"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("v106: %s has historical rows without original prepare fences; preserve the database for explicit recovery", table)
		}
	}
	// Drop this recognized invalid trigger before any table DDL. SQLite would
	// otherwise reject even unrelated ALTER TABLE statements while parsing it.
	if _, err := tx.ExecContext(ctx, `DROP TRIGGER formal_adoption_business_source`); err != nil {
		return err
	}
	// Empty legacy tables can gain the new fence FK without inventing a fence.
	if _, err := tx.ExecContext(ctx, `DROP TABLE formal_adoption_receipts; DROP TABLE formal_adoption_business`); err != nil {
		return err
	}
	for _, name := range []string{"formal_adoption_business", "formal_adoption_receipts", "formal_adoption_business_source", "formal_adoption_business_immutable", "formal_adoption_business_durable", "formal_adoption_receipt_source", "formal_adoption_receipt_immutable", "formal_adoption_receipt_durable"} {
		delete(existing, name)
	}
	for _, object := range objects {
		if _, ok := existing[object.name]; ok {
			if !replace[object.name] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DROP TRIGGER `+object.name); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, object.statement); err != nil {
			return fmt.Errorf("v106: restore %s: %w", object.name, err)
		}
	}
	return nil
}
