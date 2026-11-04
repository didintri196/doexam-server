-- Mengembalikan default kolom dan nama peran bawaan per preset lama.
ALTER TABLE tenants ALTER COLUMN preset SET DEFAULT 'school';

UPDATE roles r SET name = CASE t.preset WHEN 'school' THEN 'Guru' ELSE 'Penyusun Tes' END
  FROM tenants t
 WHERE r.tenant_id = t.id AND r.is_system AND r.code = 'author'
   AND r.name = 'Penyusun Soal' AND t.preset IN ('school', 'company');

UPDATE roles r SET name = CASE t.preset WHEN 'school' THEN 'Siswa' ELSE 'Kandidat' END
  FROM tenants t
 WHERE r.tenant_id = t.id AND r.is_system AND r.code = 'participant'
   AND r.name = 'Peserta' AND t.preset IN ('school', 'company');
