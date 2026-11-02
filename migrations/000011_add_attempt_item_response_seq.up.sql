-- client_seq dari event jawaban terakhir yang diterapkan ke attempt_items.response.
-- Event jawaban yang lebih lama (datang terlambat / tidak berurutan saat sinkron offline)
-- tidak boleh menimpa jawaban yang lebih baru.
ALTER TABLE attempt_items ADD COLUMN response_seq int NOT NULL DEFAULT 0;
