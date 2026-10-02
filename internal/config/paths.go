package config

import "github.com/Stack-Cairn/K-brain/internal/datapath"

func UserDir() (string, error) { return datapath.UserDir() }

func ProjectDir(project string) string { return datapath.ProjectDir(project) }

func MigrateProjectDir(project string) error { return datapath.MigrateProjectDir(project) }
