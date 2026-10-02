package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type ConnectionEntry struct {
	ID          string    `json:"id"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	PID         uint32    `json:"pid"`
	ProcessPath string    `json:"process_path,omitempty"`
	Protocol    string    `json:"protocol"`
	LocalAddr   string    `json:"local_addr"`
	RemoteIP    string    `json:"remote_ip"`
	RemotePort  uint16    `json:"remote_port"`
	Domain      string    `json:"domain,omitempty"`
	State       string    `json:"state"`
	Decision    string    `json:"decision"`
	BytesUp     uint64    `json:"bytes_up"`
	BytesDown   uint64    `json:"bytes_down"`
}

type ConnectionStore struct {
	db sync.RWMutex
	q  *sql.DB
}

func OpenConnectionStore(base string) (*ConnectionStore, error) {
	if base == "" {
		return nil, fmt.Errorf("connection store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", base)
	if err != nil {
		return nil, err
	}
	store := &ConnectionStore{q: db}
	for _, stmt := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=3000`,
		`CREATE TABLE IF NOT EXISTS schema_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS connections (
			id TEXT PRIMARY KEY, first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
			pid INTEGER NOT NULL, process_path TEXT NOT NULL, protocol TEXT NOT NULL,
			local_addr TEXT NOT NULL, remote_ip TEXT NOT NULL, remote_port INTEGER NOT NULL,
			domain TEXT NOT NULL, state TEXT NOT NULL, decision TEXT NOT NULL,
			bytes_up INTEGER NOT NULL, bytes_down INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_connections_last_seen ON connections(last_seen DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_connections_process ON connections(process_path)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (s *ConnectionStore) Upsert(e ConnectionEntry) error {
	if s == nil || s.q == nil {
		return nil
	}
	if e.FirstSeen.IsZero() {
		e.FirstSeen = time.Now()
	}
	if e.LastSeen.IsZero() {
		e.LastSeen = e.FirstSeen
	}
	_, err := s.q.Exec(`INSERT INTO connections
		(id,first_seen,last_seen,pid,process_path,protocol,local_addr,remote_ip,remote_port,domain,state,decision,bytes_up,bytes_down)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET last_seen=excluded.last_seen, state=excluded.state,
		bytes_up=excluded.bytes_up, bytes_down=excluded.bytes_down, domain=excluded.domain,
		process_path=excluded.process_path`,
		e.ID, e.FirstSeen.UnixNano(), e.LastSeen.UnixNano(), e.PID, e.ProcessPath,
		e.Protocol, e.LocalAddr, e.RemoteIP, e.RemotePort, e.Domain, e.State, e.Decision,
		e.BytesUp, e.BytesDown)
	return err
}

func (s *ConnectionStore) Recent(limit int) ([]ConnectionEntry, error) {
	if s == nil || s.q == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.q.Query(`SELECT id,first_seen,last_seen,pid,process_path,protocol,local_addr,
		remote_ip,remote_port,domain,state,decision,bytes_up,bytes_down
		FROM connections ORDER BY last_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ConnectionEntry
	for rows.Next() {
		var e ConnectionEntry
		var first, last int64
		if err := rows.Scan(&e.ID, &first, &last, &e.PID, &e.ProcessPath, &e.Protocol, &e.LocalAddr,
			&e.RemoteIP, &e.RemotePort, &e.Domain, &e.State, &e.Decision, &e.BytesUp, &e.BytesDown); err != nil {
			return nil, err
		}
		e.FirstSeen = time.Unix(0, first)
		e.LastSeen = time.Unix(0, last)
		result = append(result, e)
	}
	return result, rows.Err()
}

func (s *ConnectionStore) Prune(retention time.Duration) error {
	if s == nil || s.q == nil || retention <= 0 {
		return nil
	}
	_, err := s.q.Exec(`DELETE FROM connections WHERE last_seen < ?`, time.Now().Add(-retention).UnixNano())
	return err
}

func (s *ConnectionStore) Close() error {
	if s == nil || s.q == nil {
		return nil
	}
	return s.q.Close()
}

func connectionStorePath() string {
	base := os.Getenv("ProgramData")
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return filepath.Join(base, "SonDNS", "connections.db")
}
