// Package schedule holds the anchored ping schedule: the wall-clock slots at
// which a provider connection is pinged so a fresh quota window starts on time
// (port of upstream src/shared/services/quotaPingSchedule.js).
//
// With anchor 09:30, a 5h window and 3 slots/day the schedule is
// 09:30 / 14:30 / 19:30 local time. The overnight gap is what re-aligns the
// next day onto 09:30, because 24h is not a multiple of 5h.
package schedule

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	// Every target ships a single static binary and several of them (windows,
	// alpine) have no zoneinfo on disk. Without the embedded database
	// time.LoadLocation fails and every named timezone silently falls back to
	// the default — the schedule would fire at the wrong wall-clock time with
	// nothing in the log to say so.
	_ "time/tzdata"
)

const (
	// DefaultAnchor is the first slot of the day.
	DefaultAnchor = "09:30"
	// DefaultWindowHours is the spacing between slots.
	DefaultWindowHours = 5.0
	// DefaultSlotsPerDay is how many slots one anchor day carries.
	DefaultSlotsPerDay = 3
	// SlotGrace is how late a slot may still fire. A slot that came due while
	// the process was down fires once inside this window and never after it, so
	// a restart cannot replay an hour of stale pings.
	SlotGrace = 30 * time.Minute

	minWindowHours = 0.5
	maxWindowHours = 24.0
	hoursPerDay    = 24.0
)

// Schedule is a fully defaulted, validated schedule.
type Schedule struct {
	Enabled     bool    `json:"enabled"`
	Anchor      string  `json:"anchor"`
	Timezone    string  `json:"timezone"`
	WindowHours float64 `json:"windowHours"`
	SlotsPerDay int     `json:"slotsPerDay"`
}

// Input is the untrusted shape read from settings. The numeric fields are `any`
// because upstream accepts both a number and a numeric string, and a payload
// carrying "5" must not be read as "no value".
type Input struct {
	Enabled     *bool  `json:"enabled"`
	Anchor      string `json:"anchor"`
	Timezone    string `json:"timezone"`
	WindowHours any    `json:"windowHours"`
	SlotsPerDay any    `json:"slotsPerDay"`
}

// Slot is one scheduled fire time.
type Slot struct {
	// Key is "<local date>#<index>", stable across ticks so a slot that already
	// fired is recognisable on the next one.
	Key   string
	Index int
	At    time.Time
}

// LocalTimezone is the process's own zone name, used as the default so the
// dashboard shows the zone the operator is actually in.
//
// Upstream reads it from Intl (Intl.DateTimeFormat().resolvedOptions()
// .timeZone), which answers an IANA name like "Asia/Ho_Chi_Minh". Go's
// time.Local.String() answers the literal "Local" — and "Local" is a name
// time.LoadLocation accepts, so it slips through every zone check and lands
// in the settings blob as a zone no other runtime can resolve. The name is
// therefore resolved from the operating system instead.
//
// The lookup order is the one that answers correctly on each platform:
// the TZ environment variable (set by a container image or an operator),
// then /etc/localtime's symlink on Unix, then the Windows registry's
// TimeZoneKeyName. A zone that cannot be read is reported as "UTC" rather
// than as a name that only this process understands.
func LocalTimezone() string {
	if name := timezoneFromEnv(); name != "" {
		return name
	}
	if name := timezoneFromLocaltimeSymlink(); name != "" {
		return name
	}
	if name := timezoneFromRegistry(); name != "" {
		return name
	}
	return "UTC"
}

// timezoneFromEnv reads the TZ environment variable. It is the first answer on
// every platform because a container image sets it deliberately, and it
// overrides the system's own idea of the zone.
func timezoneFromEnv() string {
	tz := strings.TrimSpace(os.Getenv("TZ"))
	if tz == "" {
		return ""
	}
	// "UTC" and the empty zone are the two names every zone database agrees
	// on; anything else must resolve before it is trusted.
	if tz == "UTC" {
		return "UTC"
	}
	// time.LoadLocation special-cases "Local" and answers it with time.Local,
	// so a TZ=Local would pass the lookup below and land in the settings blob
	// as a zone name no other runtime can resolve. Go special-cases exactly
	// this one name ("" and "UTC" are handled above), and it is matched
	// case-insensitively because "local" is a different, unresolvable name
	// that must not be silently accepted either.
	if strings.EqualFold(tz, "Local") {
		return ""
	}
	if _, ok := LoadLocation(tz); ok {
		return tz
	}
	return ""
}

// timezoneFromLocaltimeSymlink reads /etc/localtime's symlink target, which
// is where Debian, Alpine and macOS put the zone name.
func timezoneFromLocaltimeSymlink() string {
	target, err := os.Readlink("/etc/localtime")
	if err != nil {
		return ""
	}
	return timezoneFromLocaltimePath(target)
}

// timezoneFromLocaltimePath extracts the zone name from a /etc/localtime
// symlink target.
//
// The target looks like "/usr/share/zoneinfo/Asia/Ho_Chi_Minh"; the zone
// name is everything after the zoneinfo directory. The path is a parameter so
// the parsing can be tested on any host — this repo's CI runs on Windows too,
// where /etc/localtime does not exist.
func timezoneFromLocaltimePath(target string) string {
	const marker = "zoneinfo/"
	idx := strings.LastIndex(target, marker)
	if idx < 0 {
		return ""
	}
	name := filepath.ToSlash(target[idx+len(marker):])
	if name == "" {
		return ""
	}
	if _, ok := LoadLocation(name); ok {
		return name
	}
	return ""
}

// timezoneFromRegistry reads the Windows time zone name from the registry.
//
// The registry records a Windows zone name ("SE Asia Standard Time"), not an
// IANA one, so the name is translated through CLDR's windowsZones.xml before
// it can be used. Without that translation the lookup answered nothing on
// every Windows install — time.LoadLocation refuses a Windows name — and the
// schedule fell back to "UTC", which put the slots a whole offset away from
// the wall clock the operator set them on.
//
// Go's x/sys/windows package is already a direct dependency of this repo — no
// new dependency is added for it.
func timezoneFromRegistry() string {
	if runtime.GOOS != "windows" {
		return ""
	}
	name := windowsTimeZoneKeyName()
	if name == "" {
		return ""
	}
	// A Windows name the tzdata understands as-is needs no translation.
	if _, ok := LoadLocation(name); ok {
		return name
	}
	return ianaZoneForWindowsZone(name)
}

// LoadLocation resolves a zone name, reporting whether it is usable.
func LoadLocation(name string) (*time.Location, bool) {
	if strings.TrimSpace(name) == "" {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil || loc == nil {
		return nil, false
	}
	return loc, true
}

// ParseAnchor splits "HH:MM" into its parts. Leading zeros are optional, an
// out-of-range hour or minute is rejected rather than clamped — a typo must not
// silently schedule the ping somewhere else.
func ParseAnchor(anchor string) (hour, minute int, ok bool) {
	raw := strings.TrimSpace(anchor)
	idx := strings.IndexByte(raw, ':')
	if idx < 1 || idx > 2 || len(raw) != idx+3 {
		return 0, 0, false
	}
	hourPart, minutePart := raw[:idx], raw[idx+1:]
	if !allDigits(hourPart) || !allDigits(minutePart) {
		return 0, 0, false
	}
	h, _ := strconv.Atoi(hourPart)
	m, _ := strconv.Atoi(minutePart)
	if h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// allDigits keeps the parser to the digits-only shape upstream's regex allows,
// so "+9:30" and " 9:3a" are rejected rather than read as something else.
func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// MaxSlotsPerDay is how many windows of this length fit in a day.
func MaxSlotsPerDay(windowHours float64) int {
	// A non-finite or non-positive window has no meaningful answer, so the
	// smallest sane one is used: int(NaN) is MinInt64 and would make every
	// caller that sizes a slice from it panic.
	if math.IsNaN(windowHours) || math.IsInf(windowHours, 0) || windowHours <= 0 {
		return 1
	}
	n := int(hoursPerDay / windowHours)
	if n < 1 {
		return 1
	}
	return n
}

// clampNumber ports upstream's clampNumber: a non-finite or unparsable value
// falls back, everything else is clamped into [min, max].
//
// The finiteness check is the load-bearing part. Go's strconv.ParseFloat
// accepts "NaN" and "Inf" — upstream's Number() rejects them, and its
// Number.isFinite guard is what keeps them out. Without the check a
// slotsPerDay of "NaN" reached int(NaN), which is MinInt64, and the slot
// list was then built with a negative capacity — a panic in the scheduler's
// tick, which takes the whole gateway down with it. The value reaches here
// from a dashboard write and from a restored backup, so it is untrusted.
func clampNumber(value any, fallback, min, max float64) float64 {
	parsed, ok := toFloat(value)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
		return fallback
	}
	if parsed < min {
		return min
	}
	if parsed > max {
		return max
	}
	return parsed
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// Normalize returns a fully-defaulted schedule, or nil when the schedule is
// absent, disabled, or its anchor is unusable and the default cannot stand in.
func Normalize(raw *Input) *Schedule {
	if raw == nil || raw.Enabled == nil || !*raw.Enabled {
		return nil
	}

	hour, minute, ok := ParseAnchor(raw.Anchor)
	if !ok {
		hour, minute, _ = ParseAnchor(DefaultAnchor)
	}

	tz := DefaultTimezoneOr(raw.Timezone)
	loc, _ := LoadLocation(tz) // Normalize already checked it
	if loc == nil {
		return nil
	}

	windowHours := clampNumber(raw.WindowHours, DefaultWindowHours, minWindowHours, maxWindowHours)
	// Upstream rounds here (Math.round), and so must this: truncating gave
	// 2 slots for a 2.6 request where upstream answered 3, so the same
	// settings produced a different number of pings per day on the two
	// runtimes. math.Round is half-away-from-zero, which is what Math.round
	// does for the positive values this clamp can produce.
	slots := int(math.Round(clampNumber(raw.SlotsPerDay, DefaultSlotsPerDay, 1, float64(MaxSlotsPerDay(windowHours)))))

	return &Schedule{
		Enabled:     true,
		Anchor:      formatClock(hour, minute),
		Timezone:    tz,
		WindowHours: windowHours,
		SlotsPerDay: slots,
	}
}

// DefaultTimezoneOr returns name when it is a usable zone and the process zone
// otherwise.
//
// "Local" is refused here for the same reason NormalizeTimezone refuses it: a
// schedule payload is stored in the settings blob and read back by another
// runtime, where the name "Local" means nothing. time.LoadLocation
// special-cases it, so without this guard the zone the env-var path rejects
// would still arrive through a settings payload.
func DefaultTimezoneOr(name string) string {
	if strings.EqualFold(name, "Local") {
		return LocalTimezone()
	}
	if _, ok := LoadLocation(name); ok {
		return name
	}
	return LocalTimezone()
}

func formatClock(hour, minute int) string {
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// anchorClock is the parsed anchor of an already-normalized schedule.
func (s *Schedule) anchorClock() (hour, minute int) {
	h, m, ok := ParseAnchor(s.Anchor)
	if !ok {
		h, m, _ = ParseAnchor(DefaultAnchor)
	}
	return h, m
}

// slotsForAnchorDay builds every slot belonging to the anchor day that contains
// day.
func (s *Schedule) slotsForAnchorDay(day time.Time) []Slot {
	loc, ok := LoadLocation(s.Timezone)
	if !ok {
		return nil
	}
	hour, minute := s.anchorClock()
	local := day.In(loc)

	// time.Date resolves the wall clock in loc, which is the DST-correct answer
	// the two-pass conversion upstream performs by hand.
	first := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	dateKey := local.Format("2006-01-02")
	spacing := time.Duration(s.WindowHours * float64(time.Hour))

	// The count is clamped here as well as in Normalize: a Schedule built by
	// hand (a test, a future caller) can carry a count no writer would
	// produce, and a negative one turns the make below into a panic. The
	// ceiling is the number of windows a day can hold, which is the largest
	// count the schedule can have any meaning for.
	count := s.SlotsPerDay
	if count < 1 {
		count = 1
	}
	if max := MaxSlotsPerDay(s.WindowHours); count > max {
		count = max
	}

	slots := make([]Slot, 0, count)
	for i := 0; i < count; i++ {
		slots = append(slots, Slot{
			Key:   dateKey + "#" + strconv.Itoa(i),
			Index: i,
			At:    first.Add(time.Duration(i) * spacing),
		})
	}
	return slots
}

// Slots returns the slots of yesterday, today and tomorrow, sorted by time.
// A day's slots never span more than 24h from its anchor, so that window covers
// everything that can already be due or next.
func (s *Schedule) Slots(now time.Time) []Slot {
	if s == nil {
		return nil
	}
	// The capacity is derived from the same clamped count the day builder
	// uses, so a hand-built Schedule with a negative count cannot turn the
	// make below into a panic.
	out := make([]Slot, 0, 3*MaxSlotsPerDay(s.WindowHours))
	for _, offset := range []time.Duration{-24 * time.Hour, 0, 24 * time.Hour} {
		out = append(out, s.slotsForAnchorDay(now.Add(offset))...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// DueSlot is the most recent slot that is due and still inside grace, or nil.
func (s *Schedule) DueSlot(now time.Time) *Slot {
	var due *Slot
	for _, slot := range s.Slots(now) {
		if slot.At.After(now) || now.Sub(slot.At) > SlotGrace {
			continue
		}
		if due == nil || slot.At.After(due.At) {
			candidate := slot
			due = &candidate
		}
	}
	return due
}

// NextSlot is the first slot in the future, or nil.
func (s *Schedule) NextSlot(now time.Time) *Slot {
	for _, slot := range s.Slots(now) {
		if slot.At.After(now) {
			candidate := slot
			return &candidate
		}
	}
	return nil
}

// SlotLabels is the "09:30 → 14:30 → 19:30" preview the schedule editor shows.
func (s *Schedule) SlotLabels() []string {
	if s == nil {
		return nil
	}
	hour, minute := s.anchorClock()
	start := hour*60 + minute
	// The count is clamped the same way slotsForAnchorDay clamps it, so a
	// hand-built Schedule cannot turn the make below into a panic.
	count := s.SlotsPerDay
	if count < 1 {
		count = 1
	}
	if max := MaxSlotsPerDay(s.WindowHours); count > max {
		count = max
	}
	labels := make([]string, 0, count)
	for i := 0; i < count; i++ {
		// Upstream rounds the offset before taking the modulo
		// (Math.round(startMinutes + index * windowHours * 60) % 1440). A
		// 5.1h window puts the third slot at 917.99999999999988631 minutes,
		// which truncates to 917 while upstream rounds it to 918 — the preview
		// showed a time the schedule never fired at.
		total := int(math.Round(float64(start)+float64(i)*s.WindowHours*60)) % 1440
		labels = append(labels, formatClock(total/60, total%60))
	}
	return labels
}
