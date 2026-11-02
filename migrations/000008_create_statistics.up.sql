-- Hasil analisis (bisa dibangun ulang dari attempt_items).

-- Statistik butir klasik per ujian atau per sesi
CREATE TABLE item_statistics (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id         uuid NOT NULL REFERENCES tenants(id),
  item_version_id   uuid NOT NULL REFERENCES item_versions(id),
  exam_id           uuid NOT NULL REFERENCES exams(id),
  session_id        uuid REFERENCES exam_sessions(id),  -- NULL = seluruh ujian
  n_responses       int NOT NULL,
  n_omitted         int NOT NULL DEFAULT 0,
  p_value           numeric(6,4),             -- tingkat kesukaran
  point_biserial    numeric(6,4),             -- daya beda
  option_stats      jsonb,                    -- {"o1": {"n": 40, "pct": 0.4, "pbis": -0.12}, ...}
  computed_at       timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX item_statistics_scope_uq
  ON item_statistics (exam_id, item_version_id, coalesce(session_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- Statistik tes keseluruhan
CREATE TABLE exam_statistics (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     uuid NOT NULL REFERENCES tenants(id),
  exam_id       uuid NOT NULL REFERENCES exams(id),
  session_id    uuid REFERENCES exam_sessions(id),
  n_attempts    int NOT NULL,
  mean_score    numeric(6,2),
  sd_score      numeric(6,2),
  min_score     numeric(6,2),
  max_score     numeric(6,2),
  median_score  numeric(6,2),
  reliability   numeric(6,4),                 -- KR-20 / Cronbach alpha
  pass_rate     numeric(6,4),
  distribution  jsonb,                        -- histogram
  computed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX exam_statistics_scope_uq
  ON exam_statistics (exam_id, coalesce(session_id, '00000000-0000-0000-0000-000000000000'::uuid));

SELECT enable_tenant_rls('item_statistics');
SELECT enable_tenant_rls('exam_statistics');
