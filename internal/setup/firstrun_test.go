package setup_test

import (
	"testing"

	"github.com/braydin72/mediaforge/internal/config"
	"github.com/braydin72/mediaforge/internal/setup"
)

func TestIsFirstRun(t *testing.T) {
	populated := &config.Config{
		Intake: config.IntakeConfig{
			WatchFolder: "/incoming",
			Library: config.IntakeLibraryConfig{
				Movies:  "/media/Movies",
				TVShows: "/media/TV Shows",
			},
		},
		APIs: config.APIsConfig{
			TMDBKey: "tmdb-key",
			TVDBKey: "tvdb-key",
		},
	}

	tests := []struct {
		name           string
		cfgFileExisted bool
		cfg            *config.Config
		want           bool
	}{
		{
			name:           "no config file triggers first run",
			cfgFileExisted: false,
			cfg:            populated,
			want:           true,
		},
		{
			name:           "empty watch folder triggers first run",
			cfgFileExisted: true,
			cfg: &config.Config{
				Intake: config.IntakeConfig{
					WatchFolder: "",
					Library: config.IntakeLibraryConfig{
						Movies:  "/media/Movies",
						TVShows: "/media/TV Shows",
					},
				},
			},
			want: true,
		},
		{
			name:           "empty movies library triggers first run",
			cfgFileExisted: true,
			cfg: &config.Config{
				Intake: config.IntakeConfig{
					WatchFolder: "/incoming",
					Library: config.IntakeLibraryConfig{
						Movies:  "",
						TVShows: "/media/TV Shows",
					},
				},
			},
			want: true,
		},
		{
			name:           "empty tv library triggers first run",
			cfgFileExisted: true,
			cfg: &config.Config{
				Intake: config.IntakeConfig{
					WatchFolder: "/incoming",
					Library: config.IntakeLibraryConfig{
						Movies:  "/media/Movies",
						TVShows: "",
					},
				},
			},
			want: true,
		},
		{
			name:           "missing TMDB key triggers first run",
			cfgFileExisted: true,
			cfg: &config.Config{
				Intake: config.IntakeConfig{
					WatchFolder: "/incoming",
					Library: config.IntakeLibraryConfig{
						Movies:  "/media/Movies",
						TVShows: "/media/TV Shows",
					},
				},
				APIs: config.APIsConfig{
					TMDBKey: "",
					TVDBKey: "tvdb-key",
				},
			},
			want: true,
		},
		{
			name:           "missing TVDB key triggers first run",
			cfgFileExisted: true,
			cfg: &config.Config{
				Intake: config.IntakeConfig{
					WatchFolder: "/incoming",
					Library: config.IntakeLibraryConfig{
						Movies:  "/media/Movies",
						TVShows: "/media/TV Shows",
					},
				},
				APIs: config.APIsConfig{
					TMDBKey: "tmdb-key",
					TVDBKey: "",
				},
			},
			want: true,
		},
		{
			name:           "fully populated config skips first run",
			cfgFileExisted: true,
			cfg:            populated,
			want:           false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := setup.IsFirstRun(tc.cfgFileExisted, tc.cfg)
			if got != tc.want {
				t.Errorf("IsFirstRun(%v, cfg) = %v, want %v", tc.cfgFileExisted, got, tc.want)
			}
		})
	}
}
