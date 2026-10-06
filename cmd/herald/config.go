package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"herald-agent/internal/channel"
	"herald-agent/internal/config"
)

func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "查看 herald 的配置",
		RunE: func(*cobra.Command, []string) error {
			return errors.New("config: missing subcommand (path|show)")
		},
	}
	configCmd.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "打印配置目录与 config.json 路径",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return configPath(cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:   "show",
			Short: "打印已保存的配置（凭据脱敏）",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return configShow(cmd.OutOrStdout())
			},
		},
	)
	return configCmd
}

func configPath(stdout io.Writer) error {
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	file, err := config.Path()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "dir:  %s\nfile: %s\n", dir, file)
	return nil
}

func configShow(stdout io.Writer) error {
	cfg, err := config.Load()
	if errors.Is(err, os.ErrNotExist) {
		path, _ := config.Path()
		fmt.Fprintf(stdout, "no configuration yet (%s)\n", path)
		return nil
	}
	if err != nil {
		return err
	}

	providers, channels := newRegistries()
	if err := cfg.Validate(providers, channels); err != nil {
		fmt.Fprintf(stdout, "configuration is invalid:\n%v\n\n", err)
	}

	printConfig(stdout, cfg, channels)
	return nil
}

// printConfig 面向人渲染配置。密钥值永不打印：凭据只显示"已设置/未设置"加上引用名，
// 这是配置层能诚实给出的最强陈述。
func printConfig(w io.Writer, cfg *config.Config, channels *channel.Registry) {
	fmt.Fprintf(w, "schema_version: %d\n", cfg.SchemaVersion)
	fmt.Fprintf(w, "default_model:  %s\n", orDash(cfg.DefaultModel))

	fmt.Fprintln(w, "\nmodels:")
	if len(cfg.Models) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, m := range cfg.Models {
		fmt.Fprintf(w, "  - %s\n", m.ID)
		fmt.Fprintf(w, "      provider: %s\n", m.Provider)
		fmt.Fprintf(w, "      protocol: %s\n", m.Protocol)
		fmt.Fprintf(w, "      base_url: %s\n", orDash(m.BaseURL))
		fmt.Fprintf(w, "      model:    %s\n", orDash(m.Model))
		fmt.Fprintf(w, "      api_key:  %s\n", credentialStatus(m.APIKeyRef))
	}

	fmt.Fprintln(w, "\nchannels:")
	if len(cfg.Channels) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, ch := range cfg.Channels {
		name := ch.Type
		if t, ok := channels.Lookup(ch.Type); ok {
			name = t.DisplayName
		}
		state := "disabled"
		if ch.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(w, "  - %s (%s, %s)\n", ch.ID, name, state)
		fmt.Fprintf(w, "      model_ref:      %s\n", orDash(ch.ModelRef))
		fmt.Fprintf(w, "      credential:     %s\n", credentialStatus(ch.CredentialRef))
	}
}

func credentialStatus(ref string) string {
	if ref == "" {
		return "未设置"
	}
	exists, err := config.CredentialExists(ref)
	switch {
	case err != nil:
		return fmt.Sprintf("状态未知 (ref: %s)", ref)
	case exists:
		return fmt.Sprintf("已设置 (ref: %s)", ref)
	default:
		return fmt.Sprintf("未设置 (ref: %s)", ref)
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
