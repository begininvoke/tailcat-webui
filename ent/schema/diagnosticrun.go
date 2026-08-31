package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DiagnosticRun stores only a terminal or in-progress run summary. Live peer
// addresses, payloads, and per-tick measurements intentionally stay out of it.
type DiagnosticRun struct{ ent.Schema }

func (DiagnosticRun) Fields() []ent.Field {
	return []ent.Field{
		idField(),
		field.String("user_id").Immutable(),
		field.String("client_id").Immutable(),
		field.Enum("kind").Values("ping", "throughput").Immutable(),
		field.Enum("status").Values("running", "succeeded", "failed", "canceled", "interrupted").Default("running"),
		field.Enum("path").Values("direct", "derp", "peer_relay").Optional(),
		field.Int64("latency_ms").Optional().Nillable(),
		field.Int64("upload_bytes").Default(0),
		field.Int64("download_bytes").Default(0),
		field.Int64("upload_bps").Default(0),
		field.Int64("download_bps").Default(0),
		field.Enum("error_code").Values(
			"diagnostic_canceled",
			"diagnostic_timeout",
			"diagnostic_invalid_magic",
			"diagnostic_header_too_large",
			"diagnostic_malformed_header",
			"diagnostic_invalid_request",
			"diagnostic_limit_exceeded",
			"diagnostic_io",
			"diagnostic_invalid_runner",
		).Optional(),
		field.Time("started_at").Default(time.Now).Immutable(),
		field.Time("finished_at").Optional().Nillable(),
	}
}

func (DiagnosticRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "started_at"),
		index.Fields("client_id", "started_at"),
	}
}

func (DiagnosticRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).Ref("diagnostic_runs").Field("user_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
		edge.From("client", TailClient.Type).Ref("diagnostic_runs").Field("client_id").Unique().Required().Immutable().Annotations(entsql.OnDelete(entsql.Cascade)),
	}
}
