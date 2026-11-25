package app

import "github.com/LemonITCN/lemon-note/lemon-note-service/internal/config"

func configHostID(cache string) (string, error) { return config.HostID(cache) }
