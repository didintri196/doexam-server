DROP TABLE IF EXISTS item_versions, items, stimulus_versions, stimuli,
  item_bank_members, item_banks, competencies, subjects;
DROP FUNCTION IF EXISTS prevent_locked_stimulus_update();
DROP FUNCTION IF EXISTS prevent_locked_version_update();
