## Table of Contents
- [Table of Contents](#table-of-contents)
- [Overview](#overview)
- [Architecture](#architecture)
- [Find us](#find-us)

Component responsbile for dequeueing incoming snapshot submissions as accepted by the libp2p listener peer. 

## Architecture

The Dequeuer is structured around three primary modules that collectively enable its functionality:

1. **Main Module(`cmd/main.go`)**:
   - This serves as the entry point for the Dequeuer component, orchestrating key operations such as initializing interfaces, retrieving and processing snapshot submission details.

2. **Configuration Module (`/config`)**:
   - The `/config` directory houses configuration files that define critical system parameters. These include client urls, contract addresses, timeout settings, security parameters, and other project-specific configurations.

3. **Package Module (`/pkgs`)**:
   - The core event processing logic resides in the `/pkgs/dequeuer` directory. These modules manage snapshot submission details retrieval, verifying and storing the snapshot submission details, forming the foundation of the Dequeuer's operations.

This modular design ensures a well-defined separation of responsibilities, with each module focusing exclusively on a distinct aspect of the system's functionality. By organizing the system into clearly delineated modules, each component can operate independently while contributing to the overall system architecture.

## Find us

* [Discord](https://powerloom.io/discord)
* [Twitter](https://twitter.com/PowerLoomHQ)
* [Github](https://github.com/PowerLoom)
* [Careers](https://wellfound.com/company/powerloom/jobs)
* [Blog](https://blog.powerloom.io/)
* [Medium Engineering Blog](https://medium.com/powerloom)
