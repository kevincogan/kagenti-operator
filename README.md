# kagenti-operator

This repository contains the Kubernetes operator for managing AI agents.

## Kagenti Operator

The Kagenti Operator, located in the [kagenti-operator/](kagenti-operator/) directory, automates the complete lifecycle management of AI agents in Kubernetes. It provides a simple, focused approach to deploying agents from container images or building them from source code.

For detailed information about the Kagenti Operator, including installation, API reference, and examples, please refer to the [README.md](kagenti-operator/README.md) in the `kagenti-operator` directory.

## Platform Operator (Deprecated)

> **⚠️ DEPRECATED**: The Platform operator has been deprecated in favor of the simpler Kagenti Operator.

The Platform operator, located in [platform-operator/](platform-operator/) directory, provided complex multi-component orchestration through Platform and Component CRs. This approach proved to be over-engineered for most use cases.

**Migration**: Use the [Kagenti Operator](kagenti-operator/README.md) with individual `Agent` CRs instead of `Platform` and `Component` CRs. This provides better alignment with Kubernetes best practices while maintaining all essential functionality.

For historical reference, see [platform-operator/README.md](platform-operator/README.md).
