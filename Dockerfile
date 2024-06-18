# syntax=docker/dockerfile:1.4

FROM rockylinux:8.9
LABEL org.opencontainers.image.authors="Lucas Ritzdorf <lritzdorf@lanl.gov>"

# Define API base URLs
## OPAAL server for auth token provisioning
ENV OPAAL_URL=http://opaal:3333

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

# Copy our helper script, which gets a token for smd and exec's Ansible
COPY ansible_shim.sh .

# TODO: This should eventually be some sort of daemon process
CMD ./ansible_shim.sh
