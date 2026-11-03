-- Istilah netral: nama peran bawaan tidak lagi memakai istilah sekolah/perusahaan.
-- Hanya peran sistem yang namanya masih bawaan preset lama yang diganti; nama yang
-- sudah diubah admin dibiarkan.
ALTER TABLE tenants ALTER COLUMN preset SET DEFAULT 'custom';

UPDATE roles SET name = 'Penyusun Soal'
 WHERE is_system AND code = 'author' AND name IN ('Guru', 'Penyusun Tes');

UPDATE roles SET name = 'Peserta'
 WHERE is_system AND code = 'participant' AND name IN ('Siswa', 'Kandidat');
