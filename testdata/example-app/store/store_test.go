package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	o, err := New([]string{"pear", "apple"})
	require.NoError(t, err)
	assert.Equal(t, []string{"apple", "pear"}, o.Items)
	assert.NotEmpty(t, o.ID)
}

func TestNewEmpty(t *testing.T) {
	_, err := New(nil)
	require.Error(t, err)
}
