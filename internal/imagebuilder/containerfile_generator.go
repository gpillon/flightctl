package imagebuilder

import (
	"encoding/base64"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// ContainerfileGenerator generates a Containerfile from ImageBuild specifications
type ContainerfileGenerator struct {
	spec                   api.ImageBuildSpec
	enrollmentCert         string // Enrollment certificate PEM
	enrollmentKey          string // Enrollment private key PEM
	defaultEnrollmentCA    string // Default CA for enrollment service
	defaultEnrollmentURL   string // Default enrollment service URL
	defaultEnrollmentUIURL string // Default enrollment UI URL
}

// NewContainerfileGenerator creates a new Containerfile generator
func NewContainerfileGenerator(spec api.ImageBuildSpec, enrollmentCert, enrollmentKey string) *ContainerfileGenerator {
	return &ContainerfileGenerator{
		spec:           spec,
		enrollmentCert: enrollmentCert,
		enrollmentKey:  enrollmentKey,
	}
}

// WithDefaultEnrollmentConfig sets default enrollment service configuration
func (g *ContainerfileGenerator) WithDefaultEnrollmentConfig(ca, serviceURL, uiURL string) *ContainerfileGenerator {
	g.defaultEnrollmentCA = ca
	g.defaultEnrollmentURL = serviceURL
	g.defaultEnrollmentUIURL = uiURL
	return g
}

// Generate creates a Containerfile from the ImageBuild spec
func (g *ContainerfileGenerator) Generate() (string, error) {
	var builder strings.Builder

	// Start with base image
	builder.WriteString(fmt.Sprintf("FROM %s\n\n", g.spec.BaseImage))

	// Create users
	if g.spec.Customizations != nil && g.spec.Customizations.Users != nil && len(*g.spec.Customizations.Users) > 0 {
		builder.WriteString("# Create users\n")
		builder.WriteString("RUN ")
		first := true
		for _, user := range *g.spec.Customizations.Users {
			userCmds, err := g.getUserCommands(user)
			if err != nil {
				return "", fmt.Errorf("failed to generate user commands: %w", err)
			}
			for _, cmd := range userCmds {
				if !first {
					builder.WriteString(" && \\\n    ")
				}
				builder.WriteString(cmd)
				first = false
			}
		}
		builder.WriteString("\n\n")
	}

	// Enable EPEL if requested
	if g.spec.Customizations != nil && g.spec.Customizations.EnableEpel != nil && *g.spec.Customizations.EnableEpel {
		builder.WriteString("# Enable EPEL repositories\n")
		builder.WriteString("RUN dnf -y install epel-release epel-next-release\n\n")
	}

	// 1. Add COPR repositories if specified
	if g.spec.Customizations != nil && g.spec.Customizations.CoprRepos != nil && len(*g.spec.Customizations.CoprRepos) > 0 {
		builder.WriteString("# Enable COPR repositories\n")
		builder.WriteString("RUN ")
		for i, repo := range *g.spec.Customizations.CoprRepos {
			if i > 0 {
				builder.WriteString(" && \\\n    ")
			}
			builder.WriteString(fmt.Sprintf("dnf copr enable -y %s", repo))
		}
		builder.WriteString("\n\n")
	}

	// 2. Add custom files
	if g.spec.Customizations != nil && g.spec.Customizations.Files != nil && len(*g.spec.Customizations.Files) > 0 {
		builder.WriteString("# Add custom files\n")
		builder.WriteString("RUN ")
		first := true
		for _, file := range *g.spec.Customizations.Files {
			fileCmds, err := g.getFileCommands(file)
			if err != nil {
				return "", fmt.Errorf("failed to generate file commands: %w", err)
			}
			for _, cmd := range fileCmds {
				if !first {
					builder.WriteString(" && \\\n    ")
				}
				builder.WriteString(cmd)
				first = false
			}
		}
		builder.WriteString("\n\n")
	}

	// 3. Add scripts and run them
	if g.spec.Customizations != nil && g.spec.Customizations.Scripts != nil && len(*g.spec.Customizations.Scripts) > 0 {
		builder.WriteString("# Add and run scripts\n")
		builder.WriteString("RUN ")
		first := true
		for _, script := range *g.spec.Customizations.Scripts {
			scriptCmds, err := g.getScriptCommands(script)
			if err != nil {
				return "", fmt.Errorf("failed to generate script commands: %w", err)
			}
			for _, cmd := range scriptCmds {
				if !first {
					builder.WriteString(" && \\\n    ")
				}
				builder.WriteString(cmd)
				first = false
			}
		}
		builder.WriteString("\n\n")
	}

	// 4. Install packages
	if g.spec.Customizations != nil && g.spec.Customizations.Packages != nil && len(*g.spec.Customizations.Packages) > 0 {
		builder.WriteString("# Install additional packages\n")
		packages := strings.Join(*g.spec.Customizations.Packages, " ")
		builder.WriteString(fmt.Sprintf("RUN dnf install -y %s && \\\n    dnf clean all\n\n", packages))
	}

	// 5. Add systemd units
	if g.spec.Customizations != nil && g.spec.Customizations.SystemdUnits != nil && len(*g.spec.Customizations.SystemdUnits) > 0 {
		builder.WriteString("# Add systemd units\n")
		builder.WriteString("RUN ")
		first := true
		for _, unit := range *g.spec.Customizations.SystemdUnits {
			unitCmds, err := g.getSystemdUnitCommands(unit)
			if err != nil {
				return "", fmt.Errorf("failed to generate systemd unit commands: %w", err)
			}
			for _, cmd := range unitCmds {
				if !first {
					builder.WriteString(" && \\\n    ")
				}
				builder.WriteString(cmd)
				first = false
			}
		}
		builder.WriteString("\n\n")
	}

	// Enable Podman service if requested
	if g.spec.Customizations != nil && g.spec.Customizations.EnablePodman != nil && *g.spec.Customizations.EnablePodman {
		builder.WriteString("# Enable Podman service\n")
		builder.WriteString("RUN systemctl enable podman.service\n\n")
	}

	// Add SSH keys for root using bootc-compatible method
	if g.spec.Customizations != nil && g.spec.Customizations.SshKeys != nil && len(*g.spec.Customizations.SshKeys) > 0 {
		builder.WriteString("# Configure SSH keys for root\n")
		builder.WriteString("RUN touch /etc/ssh/sshd_config.d/30-auth-system.conf && \\\n")
		builder.WriteString("    mkdir -p /usr/etc-system && \\\n")
		builder.WriteString("    echo 'AuthorizedKeysFile /usr/etc-system/%u.keys' >> /etc/ssh/sshd_config.d/30-auth-system.conf")
		for _, key := range *g.spec.Customizations.SshKeys {
			encodedKey := base64.StdEncoding.EncodeToString([]byte(key))
			builder.WriteString(" && \\\n    ")
			builder.WriteString(fmt.Sprintf("echo '%s' | base64 -d >> /usr/etc-system/root.keys", encodedKey))
		}
		builder.WriteString(" && \\\n    chmod 0600 /usr/etc-system/root.keys\n\n")

		// Add volume for root home
		builder.WriteString("VOLUME /var/roothome\n\n")
	}

	// Install and configure flightctl agent
	if g.spec.FlightctlConfig != nil {
		// First install the flightctl-agent package
		builder.WriteString("# Install flightctl agent\n")
		builder.WriteString("RUN ")
		installCmds := g.getFlightctlInstallCommands()
		for i, cmd := range installCmds {
			if i > 0 {
				builder.WriteString(" && \\\n    ")
			}
			builder.WriteString(cmd)
		}
		builder.WriteString("\n\n")

		// Then configure it
		builder.WriteString("# Configure flightctl agent\n")
		builder.WriteString("RUN ")
		flightctlCmds, err := g.getFlightctlConfigCommands()
		if err != nil {
			return "", fmt.Errorf("failed to generate flightctl config: %w", err)
		}
		for i, cmd := range flightctlCmds {
			if i > 0 {
				builder.WriteString(" && \\\n    ")
			}
			builder.WriteString(cmd)
		}
		builder.WriteString("\n")
	}

	return builder.String(), nil
}

// getUserCommands returns shell commands for creating a user
func (g *ContainerfileGenerator) getUserCommands(user api.ImageBuildUser) ([]string, error) {
	var cmds []string

	// Create user with specified shell
	shell := "/bin/bash"
	if user.Shell != nil && *user.Shell != "" {
		shell = *user.Shell
	}

	groupsArg := ""
	if user.Groups != nil && len(*user.Groups) > 0 {
		groupsArg = fmt.Sprintf("-G %s", strings.Join(*user.Groups, ","))
	}

	cmds = append(cmds, fmt.Sprintf("useradd -m -s %s %s %s", shell, groupsArg, user.Name))

	// Set password if provided
	if user.Password != nil && *user.Password != "" {
		// chpasswd expects format "username:password"
		passwordEntry := fmt.Sprintf("%s:%s", user.Name, *user.Password)
		encodedPassword := base64.StdEncoding.EncodeToString([]byte(passwordEntry))
		cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d | chpasswd", encodedPassword))
	}

	// Add SSH keys for user
	if user.SshKeys != nil && len(*user.SshKeys) > 0 {
		cmds = append(cmds, fmt.Sprintf("mkdir -p /home/%s/.ssh && chmod 700 /home/%s/.ssh", user.Name, user.Name))
		for _, key := range *user.SshKeys {
			encodedKey := base64.StdEncoding.EncodeToString([]byte(key))
			cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d >> /home/%s/.ssh/authorized_keys", encodedKey, user.Name))
		}
		cmds = append(cmds, fmt.Sprintf("chmod 600 /home/%s/.ssh/authorized_keys && chown -R %s:%s /home/%s/.ssh", user.Name, user.Name, user.Name, user.Name))
	}

	return cmds, nil
}

// getFileCommands returns shell commands for creating a file
func (g *ContainerfileGenerator) getFileCommands(file api.ImageBuildFile) ([]string, error) {
	var cmds []string

	// Get content (API type has Content as string, not pointer)
	content := file.Content

	// Encode content to avoid issues with special characters
	encodedContent := base64.StdEncoding.EncodeToString([]byte(content))

	// Create parent directory if needed and write content
	cmds = append(cmds, fmt.Sprintf("mkdir -p $(dirname %s)", file.Path))
	cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > %s", encodedContent, file.Path))

	// Set permissions if specified
	if file.Mode != nil && *file.Mode != "" {
		cmds = append(cmds, fmt.Sprintf("chmod %s %s", *file.Mode, file.Path))
	}

	// Set owner if specified
	if file.User != nil && *file.User != "" {
		owner := *file.User
		if file.Group != nil && *file.Group != "" {
			owner = fmt.Sprintf("%s:%s", *file.User, *file.Group)
		}
		cmds = append(cmds, fmt.Sprintf("chown %s %s", owner, file.Path))
	} else if file.Group != nil && *file.Group != "" {
		// Only group specified
		cmds = append(cmds, fmt.Sprintf("chgrp %s %s", *file.Group, file.Path))
	}

	return cmds, nil
}

// getSystemdUnitCommands returns shell commands for creating a systemd unit
func (g *ContainerfileGenerator) getSystemdUnitCommands(unit api.ImageBuildSystemdUnit) ([]string, error) {
	var cmds []string

	unitPath := fmt.Sprintf("/etc/systemd/system/%s", unit.Name)

	// Get content (API type has Content as string)
	content := unit.Content

	// Encode content to avoid issues with special characters
	encodedContent := base64.StdEncoding.EncodeToString([]byte(content))

	// Create systemd directory if needed
	cmds = append(cmds, "mkdir -p /etc/systemd/system")

	// Write unit file
	cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > %s", encodedContent, unitPath))

	// Enable the unit if requested
	if unit.Enabled != nil && *unit.Enabled {
		cmds = append(cmds, fmt.Sprintf("systemctl enable %s", unit.Name))
	}

	return cmds, nil
}

// getScriptCommands returns shell commands for creating and running a script
func (g *ContainerfileGenerator) getScriptCommands(script api.ImageBuildScript) ([]string, error) {
	var cmds []string

	// Encode script content
	encodedContent := base64.StdEncoding.EncodeToString([]byte(script.Content))

	// Create parent directory
	cmds = append(cmds, fmt.Sprintf("mkdir -p $(dirname %s)", script.Path))

	// Write script
	cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > %s", encodedContent, script.Path))

	// Make executable and run
	cmds = append(cmds, fmt.Sprintf("chmod +x %s && %s", script.Path, script.Path))

	return cmds, nil
}

// getFlightctlInstallCommands returns shell commands for installing flightctl-agent
func (g *ContainerfileGenerator) getFlightctlInstallCommands() []string {
	var cmds []string

	// Add flightctl repository
	cmds = append(cmds, "dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo")

	// Install flightctl-agent
	cmds = append(cmds, "dnf -y install flightctl-agent")

	// Clean dnf cache
	cmds = append(cmds, "dnf -y clean all")

	// Enable flightctl-agent service
	cmds = append(cmds, "systemctl enable flightctl-agent.service")

	return cmds
}

// getFlightctlConfigCommands returns shell commands for configuring flightctl agent
func (g *ContainerfileGenerator) getFlightctlConfigCommands() ([]string, error) {
	var cmds []string

	// Generate the config YAML
	configYaml := g.buildFlightctlConfigYaml()

	// Encode config to base64
	encodedConfig := base64.StdEncoding.EncodeToString([]byte(configYaml))

	// Create config directory and write config file
	cmds = append(cmds, "mkdir -p /etc/flightctl")
	cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > /etc/flightctl/config.yaml", encodedConfig))

	// Add enrollment certificate if provided
	if g.enrollmentCert != "" {
		// Encode certificate in base64 to avoid shell quoting issues
		encodedCert := base64.StdEncoding.EncodeToString([]byte(g.enrollmentCert))
		cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > /etc/flightctl/enrollment-cert.pem", encodedCert))
	}

	// Add enrollment key if provided
	if g.enrollmentKey != "" {
		// Encode key in base64 to avoid shell quoting issues
		encodedKey := base64.StdEncoding.EncodeToString([]byte(g.enrollmentKey))
		cmds = append(cmds, fmt.Sprintf("echo '%s' | base64 -d > /etc/flightctl/enrollment-key.pem", encodedKey))

		// Set proper permissions on the key file
		cmds = append(cmds, "chmod 600 /etc/flightctl/enrollment-key.pem")
	}

	return cmds, nil
}

func (g *ContainerfileGenerator) buildFlightctlConfigYaml() string {
	var config strings.Builder

	fc := g.spec.FlightctlConfig

	// Enrollment service configuration (enrollment-service, not enrollmentService)
	// Include enrollment service if:
	// 1. overrideEnrollmentService is true AND enrollmentService is specified by user
	// 2. overrideEnrollmentService is false/unset AND we have default values (cert can be placeholder for preview)

	useUserProvidedEnrollment := fc.OverrideEnrollmentService != nil && *fc.OverrideEnrollmentService && fc.EnrollmentService != nil
	useDefaultEnrollment := (fc.OverrideEnrollmentService == nil || !*fc.OverrideEnrollmentService) &&
		g.defaultEnrollmentURL != "" // Don't require cert here - it will be added dynamically during build

	if useUserProvidedEnrollment || useDefaultEnrollment {
		config.WriteString("enrollment-service:\n")

		// Authentication section (use dynamic cert if available, placeholder for preview, or user-provided)
		if g.enrollmentCert != "" && g.enrollmentKey != "" {
			// Use dynamically generated enrollment certificate and key (during actual build)
			config.WriteString("  authentication:\n")
			config.WriteString("    client-certificate: /etc/flightctl/enrollment-cert.pem\n")
			config.WriteString("    client-key: /etc/flightctl/enrollment-key.pem\n")
		} else if useDefaultEnrollment {
			// Preview mode: use placeholder for certificate and key (will be generated during actual build)
			config.WriteString("  authentication:\n")
			config.WriteString("    client-certificate-data: <ENROLLMENT_CERTIFICATE_WILL_BE_GENERATED_DURING_BUILD>\n")
			config.WriteString("    client-key-data: <ENROLLMENT_KEY_WILL_BE_GENERATED_DURING_BUILD>\n")
		} else if useUserProvidedEnrollment && fc.EnrollmentService.Authentication.ClientCertificateData != "" {
			// Use user-provided authentication
			config.WriteString("  authentication:\n")
			config.WriteString(fmt.Sprintf("    client-certificate-data: %s\n", fc.EnrollmentService.Authentication.ClientCertificateData))
			if fc.EnrollmentService.Authentication.ClientKeyData != "" {
				config.WriteString(fmt.Sprintf("    client-key-data: %s\n", fc.EnrollmentService.Authentication.ClientKeyData))
			}
		}

		// Service section
		config.WriteString("  service:\n")
		if useDefaultEnrollment {
			// Use default values from deployment (real values even in preview)
			if g.defaultEnrollmentCA != "" {
				config.WriteString(fmt.Sprintf("    certificate-authority-data: %s\n", g.defaultEnrollmentCA))
			}
			config.WriteString(fmt.Sprintf("    server: %s\n", g.defaultEnrollmentURL))
		} else if useUserProvidedEnrollment && fc.EnrollmentService.Service.Server != "" {
			// Use user-provided values
			if fc.EnrollmentService.Service.CertificateAuthorityData != "" {
				config.WriteString(fmt.Sprintf("    certificate-authority-data: %s\n", fc.EnrollmentService.Service.CertificateAuthorityData))
			}
			config.WriteString(fmt.Sprintf("    server: %s\n", fc.EnrollmentService.Service.Server))
		}

		// Enrollment UI endpoint
		if useDefaultEnrollment && g.defaultEnrollmentUIURL != "" {
			config.WriteString(fmt.Sprintf("  enrollment-ui-endpoint: %s\n", g.defaultEnrollmentUIURL))
		} else if useUserProvidedEnrollment && fc.EnrollmentService.EnrollmentUiEndpoint != "" {
			config.WriteString(fmt.Sprintf("  enrollment-ui-endpoint: %s\n", fc.EnrollmentService.EnrollmentUiEndpoint))
		}
	}

	// Management service configuration (management-service, not managementService)
	// Always include it, even if empty
	config.WriteString("management-service:\n")
	config.WriteString("  authentication: {}\n")
	config.WriteString("  service: {}\n")

	// Agent configuration
	if fc.SpecFetchInterval != nil && *fc.SpecFetchInterval != "" {
		config.WriteString(fmt.Sprintf("spec-fetch-interval: %s\n", *fc.SpecFetchInterval))
	}
	if fc.StatusUpdateInterval != nil && *fc.StatusUpdateInterval != "" {
		config.WriteString(fmt.Sprintf("status-update-interval: %s\n", *fc.StatusUpdateInterval))
	}

	// Default labels (default-labels, not defaultLabels)
	if fc.DefaultLabels != nil && len(*fc.DefaultLabels) > 0 {
		config.WriteString("default-labels:\n")
		for k, v := range *fc.DefaultLabels {
			config.WriteString(fmt.Sprintf("  %s: %s\n", k, v))
		}
	}

	// System info configuration (system-info, not systemInfo)
	if fc.SystemInfo != nil && len(*fc.SystemInfo) > 0 {
		config.WriteString("system-info:\n")
		for _, info := range *fc.SystemInfo {
			config.WriteString(fmt.Sprintf("  - %s\n", info))
		}
	}

	if fc.SystemInfoCustom != nil && len(*fc.SystemInfoCustom) > 0 {
		config.WriteString("system-info-custom:\n")
		for _, info := range *fc.SystemInfoCustom {
			config.WriteString(fmt.Sprintf("  - %s\n", info))
		}
	}

	// Timeouts and intervals (kebab-case)
	if fc.SystemInfoTimeout != nil && *fc.SystemInfoTimeout != "" {
		config.WriteString(fmt.Sprintf("system-info-timeout: %s\n", *fc.SystemInfoTimeout))
	}
	if fc.PullTimeout != nil && *fc.PullTimeout != "" {
		config.WriteString(fmt.Sprintf("pull-timeout: %s\n", *fc.PullTimeout))
	}

	// Log level (log-level, not logLevel)
	if fc.LogLevel != nil && *fc.LogLevel != "" {
		config.WriteString(fmt.Sprintf("log-level: %s\n", *fc.LogLevel))
	}

	// TPM configuration
	if fc.Tpm != nil && fc.Tpm.Enabled != nil && *fc.Tpm.Enabled {
		config.WriteString("tpm:\n")
		config.WriteString("  enabled: true\n")
		if fc.Tpm.DevicePath != nil && *fc.Tpm.DevicePath != "" {
			config.WriteString(fmt.Sprintf("  device-path: %s\n", *fc.Tpm.DevicePath))
		}
		if fc.Tpm.AuthEnabled != nil {
			config.WriteString(fmt.Sprintf("  auth-enabled: %t\n", *fc.Tpm.AuthEnabled))
		}
		if fc.Tpm.StorageFilePath != nil && *fc.Tpm.StorageFilePath != "" {
			config.WriteString(fmt.Sprintf("  storage-file-path: %s\n", *fc.Tpm.StorageFilePath))
		}
	}

	return config.String()
}
