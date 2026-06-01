package main

import (
	"testing"
	"time"
)

func TestCache_SetAndGet(t *testing.T) {
	cache := NewCache(true, Duration(1*time.Hour))

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)

	result, found := cache.Get(key)
	if !found {
		t.Fatal("expected to find key in cache")
	}
	if result != value {
		t.Errorf("expected %v, got %v", value, result)
	}
}

func TestCache_Disabled(t *testing.T) {
	cache := NewCache(false, Duration(1*time.Hour))

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)

	_, found := cache.Get(key)
	if found {
		t.Error("expected not to find key in disabled cache")
	}
}

func TestCache_Delete(t *testing.T) {
	cache := NewCache(true, Duration(1*time.Hour))

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)
	cache.Delete(key)

	_, found := cache.Get(key)
	if found {
		t.Error("expected key to be deleted")
	}
}

func TestCache_Clear(t *testing.T) {
	cache := NewCache(true, Duration(1*time.Hour))

	cache.Set("key1", "value1")
	cache.Set("key2", "value2")
	cache.Clear()

	_, found1 := cache.Get("key1")
	_, found2 := cache.Get("key2")

	if found1 || found2 {
		t.Error("expected cache to be cleared")
	}
}

func TestCache_Expiration(t *testing.T) {
	shortTTL := Duration(100 * time.Millisecond)
	cache := NewCache(true, shortTTL)

	key := "test-key"
	value := "test-value"

	cache.Set(key, value)

	_, found := cache.Get(key)
	if !found {
		t.Error("expected key to exist immediately")
	}

	time.Sleep(150 * time.Millisecond)

	_, found = cache.Get(key)
	if found {
		t.Error("expected key to be expired")
	}
}

func TestCache_MultipleItems(t *testing.T) {
	cache := NewCache(true, Duration(1*time.Hour))

	cache.Set("key1", "value1")
	cache.Set("key2", "value2")
	cache.Set("key3", "value3")

	if _, found := cache.Get("key1"); !found {
		t.Error("expected key1")
	}
	if _, found := cache.Get("key2"); !found {
		t.Error("expected key2")
	}
	if _, found := cache.Get("key3"); !found {
		t.Error("expected key3")
	}
}

func TestCache_Overwrite(t *testing.T) {
	cache := NewCache(true, Duration(1*time.Hour))

	key := "test-key"

	cache.Set(key, "value1")
	cache.Set(key, "value2")

	result, found := cache.Get(key)
	if !found {
		t.Fatal("expected key to exist")
	}
	if result != "value2" {
		t.Errorf("expected value2, got %v", result)
	}
}
