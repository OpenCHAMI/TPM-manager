# syntax=docker/dockerfile:1.4

# Compile our webserver and Ansible launcher
FROM rockylinux:8.9 AS builder
RUN dnf install -y go
COPY *.go go.* .
RUN go build .


# Build the actual image
FROM rockylinux:8.9
LABEL org.opencontainers.image.authors="Lucas Ritzdorf <lritzdorf@lanl.gov>"

# Define API base URLs
## TPM-manager webserver's port for POST requests
ARG TPM_PORT=27730
ENV TPM_PORT=$TPM_PORT
## OPAAL server for auth token provisioning
ENV OPAAL_URL=http://opaal:3333
## SMD server for node inventory retrieval
ENV HSM_URL=http://smd:27779

# Get Ansible-related packages, and clean up after ourselves
# NOTE: If these don't happen in the same command, they become separate layers
# and don't use any less space.
RUN dnf install -y epel-release \
 && dnf install -y jq ansible python3.12-requests \
 && dnf clean all && rm -r /var/cache/dnf/

# Copy the smd inventory plugin into Ansible's system-level plugins directory
COPY ansible-smd-inventory/smd_inventory.py /usr/share/ansible/plugins/inventory/

# Grab all the Ansible things
COPY ansible/ ansible/
WORKDIR ansible

# Copy our webserver/launcher
COPY --from=builder TPM-manager .

# Expose webserver's port for POST requests
EXPOSE $TPM_PORT

# Run the webserver/launcher
CMD ./TPM-manager -batch-size 100 -interval 5m -playbook main.yaml -port $TPM_PORT
