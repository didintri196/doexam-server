-- Ukuran gambar disimpan supaya klien bisa menyiapkan ruang sebelum gambar dimuat
-- (tata letak soal tidak melompat). NULL untuk audio/video dan file lama.
ALTER TABLE media_files ADD COLUMN width int CHECK (width > 0), ADD COLUMN height int CHECK (height > 0);
