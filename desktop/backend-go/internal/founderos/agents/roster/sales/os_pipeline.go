package sales

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rhl/businessos-backend/internal/founderos/agents"
	"github.com/rhl/businessos-backend/internal/founderos/connectors"
)

type osPipeline struct {
	Pool       *pgxpool.Pool
	Workspaces []string
}

func (s osPipeline) Status(ctx context.Context) connectors.Status {
	status := connectors.Status{ID: "os-pipeline", Name: "Synced OS pipeline", State: connectors.StateNotConfigured}
	if s.Pool == nil {
		status.Detail = "OS database is unavailable"
		return status
	}
	ids := []string{}
	for _, id := range s.Workspaces {
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		status.Detail = "Pipeline workspace is unresolved"
		return status
	}
	var total, open int
	err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status<>'converted') FROM founderos_funnel_contacts WHERE workspace_id=ANY($1::uuid[])`, ids).Scan(&total, &open)
	if err != nil {
		status.State = connectors.StateError
		status.Detail = "OS pipeline could not be read"
		return status
	}
	status.State = connectors.StateConnected
	status.Detail = fmt.Sprintf("%d synced leads · %d open", total, open)
	status.Meta = map[string]any{"leads": total, "open": open}
	return status
}

type osPipelineLane struct {
	meta   agents.Meta
	Source connectors.Connector
}

func (a *osPipelineLane) Meta() agents.Meta { return a.meta }
func (a *osPipelineLane) Run(ctx context.Context) (agents.Result, error) {
	s := a.Source.Status(ctx)
	return agents.Result{OK: s.State == connectors.StateConnected, Summary: a.meta.Name + " · " + s.Detail, Data: s.Meta}, nil
}
