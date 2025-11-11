package imagebuilder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestNewStorageManager(t *testing.T) {
	require := require.New(t)

	cfg := &config.Config{}
	log := logrus.New()

	sm := NewStorageManager(cfg, log)

	require.NotNil(sm)
	require.Equal(cfg, sm.config)
	require.Equal(log, sm.log)
}

func TestGetS3Region(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name     string
		region   string
		expected string
	}{
		{
			name:     "empty region defaults to us-east-1",
			region:   "",
			expected: "us-east-1",
		},
		{
			name:     "custom region",
			region:   "us-west-2",
			expected: "us-west-2",
		},
		{
			name:     "eu-west-1 region",
			region:   "eu-west-1",
			expected: "eu-west-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getS3Region(tt.region)
			require.Equal(tt.expected, result)
		})
	}
}

func TestCopyFile(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	sourceFile := filepath.Join(tmpDir, "source.txt")
	destFile := filepath.Join(tmpDir, "dest.txt")

	// Create source file
	testContent := "test content for file copy"
	err := os.WriteFile(sourceFile, []byte(testContent), 0644)
	require.NoError(err)

	// Copy file
	size, err := copyFile(sourceFile, destFile)
	require.NoError(err)
	require.Equal(int64(len(testContent)), size)

	// Verify destination file exists and has correct content
	destContent, err := os.ReadFile(destFile)
	require.NoError(err)
	require.Equal(testContent, string(destContent))
}

func TestCopyFile_NonExistentSource(t *testing.T) {
	require := require.New(t)

	tmpDir := t.TempDir()
	sourceFile := filepath.Join(tmpDir, "nonexistent.txt")
	destFile := filepath.Join(tmpDir, "dest.txt")

	_, err := copyFile(sourceFile, destFile)
	require.Error(err)
}

func TestGetImageExtensionFromString_Storage(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name      string
		imageType string
		expected  string
	}{
		{
			name:      "iso extension",
			imageType: "iso",
			expected:  "iso",
		},
		{
			name:      "qcow2 extension",
			imageType: "qcow2",
			expected:  "qcow2",
		},
		{
			name:      "ami extension maps to raw",
			imageType: "ami",
			expected:  "raw",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getImageExtensionFromString(tt.imageType)
			require.Equal(tt.expected, result)
		})
	}
}

