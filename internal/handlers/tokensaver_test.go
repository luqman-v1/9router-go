package handlers

import (
	"testing"
)

func TestNewTokenSaverConfig(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if c.RTKEnabled() || c.CavemanEnabled() || c.PonytailEnabled() {
		t.Error("expected all false")
	}
	c2 := NewTokenSaverConfig(true, true, true)
	if !c2.RTKEnabled() || !c2.CavemanEnabled() || !c2.PonytailEnabled() {
		t.Error("expected all true")
	}
	c3 := NewTokenSaverConfig(true, false, true)
	if !c3.RTKEnabled() {
		t.Error("expected RTK true")
	}
	if c3.CavemanEnabled() {
		t.Error("expected Caveman false")
	}
	if !c3.PonytailEnabled() {
		t.Error("expected Ponytail true")
	}
}

func TestSetRTK(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	if c.RTKEnabled() {
		t.Error("expected initial false")
	}
	c.SetRTK(true)
	if !c.RTKEnabled() {
		t.Error("expected true after SetRTK(true)")
	}
	c.SetRTK(false)
	if c.RTKEnabled() {
		t.Error("expected false after SetRTK(false)")
	}
}

func TestSetCaveman(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetCaveman(true)
	if !c.CavemanEnabled() {
		t.Error("expected true after SetCaveman(true)")
	}
	c.SetCaveman(false)
	if c.CavemanEnabled() {
		t.Error("expected false after SetCaveman(false)")
	}
}

func TestSetPonytail(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetPonytail(true)
	if !c.PonytailEnabled() {
		t.Error("expected true after SetPonytail(true)")
	}
	c.SetPonytail(false)
	if c.PonytailEnabled() {
		t.Error("expected false after SetPonytail(false)")
	}
}

func TestSnapshot(t *testing.T) {
	c := NewTokenSaverConfig(true, false, true)
	rtk, caveman, ponytail := c.Snapshot()
	if rtk != true {
		t.Errorf("expected rtk=true, got %v", rtk)
	}
	if caveman != false {
		t.Errorf("expected caveman=false, got %v", caveman)
	}
	if ponytail != true {
		t.Errorf("expected ponytail=true, got %v", ponytail)
	}
}

func TestSetAll(t *testing.T) {
	c := NewTokenSaverConfig(false, false, false)
	c.SetAll(true, true, false)
	rtk, caveman, ponytail := c.Snapshot()
	if rtk != true || caveman != true || ponytail != false {
		t.Errorf("SetAll mismatch: got (%v,%v,%v), want (true,true,false)", rtk, caveman, ponytail)
	}
}
