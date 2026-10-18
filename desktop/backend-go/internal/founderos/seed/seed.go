// Package seed ships the FounderOS demo data: v1's seeded SQLite databases,
// dumped as reviewable SQL text, materialized on demand and loaded into
// Postgres by the ETL (cmd/founderos-seed).
package seed

import (
	"database/sql"
	"embed"
	"fmt"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed demo/*.sql tfg/*.sql client/*.sql
var demoFS embed.FS

// Profiles are the data sets a seed can load: "demo" is upstream's demo
// operator; "tfg" is The Founders Group (seed/tfg/*.sql applied on top of the
// demo schema: demo records removed, TFG team, agents, SOPs and roadmap in).
// "client" is a TFG client install: the tfg overlay, then seed/client/*.sql
// (TFG's own team, SOPs and roadmap removed).
var Profiles = []string{"demo", "tfg", "client"}

// DumpedOn is the day the fixtures were captured from v1's seed.
const DumpedOn = "2026-10-09"

// Files are the SQLite databases the ETL reads (etl.FileOS and its side files).
var Files = []string{"founderos-os", "bank", "ledger", "paykit"}

// relative lists the columns v1 seeds relative to "now" (funnel touch days,
// agent-run history). They move forward by the days since DumpedOn so the
// demo reads as current whenever it is seeded.
var relative = []struct {
	table, col string
	dateOnly   bool
}{
	{"agent_runs", "started_at", false},
	{"agent_runs", "finished_at", false},
	{"funnel_contacts", "created_at", true},
	{"funnel_touches", "at", true},
}

// Materialize writes <name>.db for every demo database into dir, with the
// relative dates shifted to now.
func Materialize(dir string, now time.Time) error {
	return MaterializeProfile(dir, now, "demo")
}

// MaterializeProfile is Materialize for a profile: for "tfg", each database's
// seed/tfg/<name>.sql overlay runs after the demo dump.
func MaterializeProfile(dir string, now time.Time, profile string) error {
	switch profile {
	case "demo", "tfg", "client":
	default:
		return fmt.Errorf("seed: unknown profile %q (want one of %v)", profile, Profiles)
	}
	dumped, err := time.Parse("2006-01-02", DumpedOn)
	if err != nil {
		return err
	}
	days := int(now.UTC().Truncate(24*time.Hour).Sub(dumped).Hours() / 24)
	for _, name := range Files {
		dump, err := demoFS.ReadFile("demo/" + name + ".sql")
		if err != nil {
			return err
		}
		for _, layer := range overlays(profile) {
			overlay, err := demoFS.ReadFile(layer + "/" + name + ".sql")
			if err != nil {
				return fmt.Errorf("%s: %s overlay: %w", name, layer, err)
			}
			dump = append(append(dump, '\n'), overlay...)
		}
		if err := materialize(filepath.Join(dir, name+".db"), string(dump), days); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// overlays are the SQL layers a profile applies on top of the demo dump.
func overlays(profile string) []string {
	switch profile {
	case "tfg":
		return []string{"tfg"}
	case "client":
		return []string{"tfg", "client"}
	}
	return nil
}

func materialize(path, dump string, days int) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(dump); err != nil {
		return err
	}
	if days == 0 {
		return nil
	}
	mod := fmt.Sprintf("%+d days", days)
	for _, r := range relative {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, r.table).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		expr := fmt.Sprintf(`strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', "%s", ?)`, r.col)
		if r.dateOnly {
			expr = fmt.Sprintf(`date("%s", ?)`, r.col)
		}
		q := fmt.Sprintf(`UPDATE "%s" SET "%s" = %s WHERE "%s" IS NOT NULL AND "%s" <> ''`, r.table, r.col, expr, r.col, r.col)
		if _, err := db.Exec(q, mod); err != nil {
			return fmt.Errorf("shift %s.%s: %w", r.table, r.col, err)
		}
	}
	return nil
}
