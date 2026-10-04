package db

import (
	"database/sql"

	"9router/proxy/internal/models"
)

// comboColumns is the shared column list for every combos read.
const comboColumns = `id, name, kind, models, createdAt, updatedAt`

// defaultComboStrategy is what a combo gets when neither a per-combo override
// nor the global setting names one.
const defaultComboStrategy = "fallback"

// scanCombo fills a combo from a row shaped like comboColumns.
func scanCombo(scan func(dest ...any) error, combo *models.Combo) error {
	return scan(&combo.ID, &combo.Name, &combo.Kind, &combo.Models, &combo.CreatedAt, &combo.UpdatedAt)
}

// resolveComboStrategy picks the strategy for one combo: a per-combo override
// wins, then the global default, then "fallback". A nil settings read (no
// settings row yet) leaves the fallback in place rather than failing the read.
func resolveComboStrategy(combo *models.Combo, s *SettingsData) {
	combo.Strategy = defaultComboStrategy
	if s == nil {
		return
	}
	if cs, ok := s.ComboStrategies[combo.Name]; ok && cs.Strategy != "" {
		combo.Strategy = cs.Strategy
		return
	}
	if s.ComboStrategy != "" {
		combo.Strategy = s.ComboStrategy
	}
}

// GetComboByName retrieves a combo configuration by its name.
func (r *Repo) GetComboByName(name string) (*models.Combo, error) {
	var combo models.Combo
	err := scanCombo(r.db.QueryRow(
		"SELECT "+comboColumns+" FROM combos WHERE name = ? LIMIT 1",
		name,
	).Scan, &combo)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	settings, _ := r.GetSettings()
	resolveComboStrategy(&combo, settings)
	return &combo, nil
}

// GetComboById retrieves a combo configuration by its ID.
func (r *Repo) GetComboById(id string) (*models.Combo, error) {
	var combo models.Combo
	err := scanCombo(r.db.QueryRow(
		"SELECT "+comboColumns+" FROM combos WHERE id = ? LIMIT 1",
		id,
	).Scan, &combo)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	settings, _ := r.GetSettings()
	resolveComboStrategy(&combo, settings)
	return &combo, nil
}

// GetCombos retrieves all combos from the database.
func (r *Repo) GetCombos() ([]*models.Combo, error) {
	rows, err := r.db.Query("SELECT " + comboColumns + " FROM combos ORDER BY createdAt ASC")
	if err != nil {
		return nil, err
	}
	// Drain and close the rows BEFORE reading settings: holding an open rows set
	// occupies one pooled connection (MaxOpenConns is 4), so a nested query
	// here could deadlock the whole pool once four concurrent callers pile up.
	var combos []*models.Combo
	for rows.Next() {
		var combo models.Combo
		if err := scanCombo(rows.Scan, &combo); err != nil {
			rows.Close()
			return nil, err
		}
		combos = append(combos, &combo)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	settings, _ := r.GetSettings()

	// Strategy resolution runs after the rows are closed so the nested settings
	// read can never hold two pooled connections at once.
	for _, combo := range combos {
		resolveComboStrategy(combo, settings)
	}
	return combos, nil
}