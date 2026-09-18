# syntax=docker/dockerfile:1.7
ARG BASE_PLATFORM=linux/amd64
FROM --platform=$BASE_PLATFORM public.ecr.aws/ubuntu/ubuntu:24.04@sha256:be20a0347f238b7d373edddc55923443b21dd9a60277bf8a93e43458cd0bf2fc

ARG RUNNER_IMAGES_REV=e986db797519f06a2e5e53701a715cfa4c1545e8
ARG RUNNER_IMAGES_ARCHIVE_SHA256=e87ede55bbdee21ee92113124a598e64d23a21e322f65f45e3a874c563f9f4cb
ARG AZCOPY_VERSION=10.32.6
ARG AZCOPY_DEB_SHA256=1a5078a8260ba7524a4400c602519d36905e592cd87a1db91a30af0da528fc86
ARG AZURE_CLI_VERSION=2.88.0
ARG AZURE_CLI_JAMMY_DEB_SHA256=4decc8359ba3542becf2686474e3d068c2fc0b9bb9ec64cbcc8f5aa0cb7c2b61
ARG AZURE_CLI_NOBLE_DEB_SHA256=dedb0d666ad557edce8548e025c36fa3a28ac00df4f3d1e889e1246d2a261c36
ARG AZURE_DEVOPS_EXTENSION_VERSION=1.0.6
ARG AZURE_DEVOPS_EXTENSION_SHA256=fa779e1fd6e6e1b726c3656b6a1968537c208041c6af54d2a7476772d896b34b
ARG BICEP_VERSION=0.45.15
ARG BICEP_NUGET_SHA256=2055d8e4b2b984591d06b8c054dfb6db3b68f2bf236309ae185f06a161ae714a
ARG GOOGLE_CLOUD_CLI_VERSION=577.0.0
ARG GOOGLE_CLOUD_CLI_ARCHIVE_SHA256=0b32d330446ce7b0f57f253e7efab4636c18fb1f87a3ac31c6c3f2a2a697525e
ARG NVM_VERSION=0.40.6
ARG NVM_ARCHIVE_SHA256=17302cad7feedb1ad33ba738f93d2176a90970724f22de119603624fcbdec1a2
ARG AWS_CLI_VERSION=2.33.2
ARG AWS_CLI_ARCHIVE_SHA256=03a62592085c43974bbb795c74df0da0345041cbceb97a22b29b04e5b5176a10
ARG AWS_SESSION_MANAGER_PLUGIN_VERSION=1.2.764.0
ARG AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256=beed4c95c42afd29756d9ecea59c3fcbf937b2c35b9ef84d12b93ac6e74726ba
ARG AWS_SAM_CLI_VERSION=1.151.0
ARG AWS_SAM_CLI_ARCHIVE_SHA256=679c54a86512e0f73616856d460b81b438fd5b9b004de5dcb624892dddfbb584
ARG AWS_SAM_CLI_ARCHIVE_SIZE=207357695
ARG GITHUB_CLI_VERSION=2.85.0
ARG GITHUB_CLI_DEB_SHA256=4ed5ff89ef53da00af9d93ac1beaa5665694f18cee8cc8d644a201541d43148c
ARG YQ_VERSION=4.50.1
ARG YQ_BINARY_SHA256=c7a1278e6bbc4924f41b56db838086c39d13ee25dcb22089e7fbf16ac901f0d4
ARG ZSTD_VERSION=1.5.7
ARG ZSTD_ARCHIVE_SHA256=eb33e51f49a15e023950cd7825ca74a4a2b43db8354825ac24fc1b7ee09e6fa3
ARG DOCKER_GPG_SHA256=1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570
ARG DOCKER_GPG_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

ENV DEBIAN_FRONTEND=noninteractive \
    ImageOS=ubuntu24 \
    IMAGE_TEMPLATE=github-runner-ubuntu-slim \
    RUNNER_TOOL_CACHE=/opt/hostedtoolcache \
    AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache \
    HOME=/home/runner \
    NVM_DIR=/home/runner/.nvm \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

COPY common/scripts/setup-common.sh /usr/local/share/qiniu-sandbox-runner-template/setup-common.sh
COPY github-runner-ubuntu-slim/scripts/setup-template.sh /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
COPY common/scripts/ensure-docker /usr/local/share/qiniu-sandbox-runner-template/ensure-docker
COPY common/scripts/download-checked-range /usr/local/share/qiniu-sandbox-runner-template/download-checked-range
COPY common/scripts/curl /usr/local/share/qiniu-sandbox-runner-template/curl

RUN TEMPLATE_FLAVOR=slim RUNNER_TEMPLATE_PHASE=bootstrap bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN install -d -m 0755 /opt/qiniu-runner-build-cache
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-00 0 16777215
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-01 16777216 33554431
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-02 33554432 50331647
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-03 50331648 67108863
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-04 67108864 83886079
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-05 83886080 100663295
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-06 100663296 117440511
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-07 117440512 134217727
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-08 134217728 150994943
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-09 150994944 167772159
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-10 167772160 184549375
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-11 184549376 201326591
RUN bash /usr/local/share/qiniu-sandbox-runner-template/download-checked-range \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-12 201326592 207357694
RUN set -eux; \
    cat /opt/qiniu-runner-build-cache/aws-sam-cli.part-* > /tmp/qiniu-aws-sam-cli.zip; \
    test "$(wc -c </tmp/qiniu-aws-sam-cli.zip | tr -d '[:space:]')" -eq "$AWS_SAM_CLI_ARCHIVE_SIZE"; \
    echo "$AWS_SAM_CLI_ARCHIVE_SHA256  /tmp/qiniu-aws-sam-cli.zip" | sha256sum --check -; \
    rm -f /opt/qiniu-runner-build-cache/aws-sam-cli.part-*; \
    rmdir /opt/qiniu-runner-build-cache; \
    TEMPLATE_FLAVOR=slim RUNNER_TEMPLATE_PHASE=platform bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN TEMPLATE_FLAVOR=slim RUNNER_TEMPLATE_PHASE=toolchain bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN set -eux; \
    rm -rf /home/runner/.nvm; \
    cp -a /etc/skel/.nvm /home/runner/.nvm; \
    chown -R runner:runner /home/runner/.nvm
COPY common/actions-runner.env /usr/local/share/qiniu-sandbox-runner-template/actions-runner.env
COPY common/scripts/assemble-runner-archive /usr/local/share/qiniu-sandbox-runner-template/assemble-runner-archive
RUN install -d -m 0755 /opt/qiniu-runner-build-cache/actions-runner
COPY common/.build/actions-runner/part-000 /opt/qiniu-runner-build-cache/actions-runner/part-000
COPY common/.build/actions-runner/part-001 /opt/qiniu-runner-build-cache/actions-runner/part-001
COPY common/.build/actions-runner/part-002 /opt/qiniu-runner-build-cache/actions-runner/part-002
COPY common/.build/actions-runner/part-003 /opt/qiniu-runner-build-cache/actions-runner/part-003
COPY common/.build/actions-runner/part-004 /opt/qiniu-runner-build-cache/actions-runner/part-004
COPY common/.build/actions-runner/part-005 /opt/qiniu-runner-build-cache/actions-runner/part-005
COPY common/.build/actions-runner/part-006 /opt/qiniu-runner-build-cache/actions-runner/part-006
COPY common/.build/actions-runner/part-007 /opt/qiniu-runner-build-cache/actions-runner/part-007
COPY common/.build/actions-runner/part-008 /opt/qiniu-runner-build-cache/actions-runner/part-008
COPY common/.build/actions-runner/part-009 /opt/qiniu-runner-build-cache/actions-runner/part-009
COPY common/.build/actions-runner/part-010 /opt/qiniu-runner-build-cache/actions-runner/part-010
COPY common/.build/actions-runner/part-011 /opt/qiniu-runner-build-cache/actions-runner/part-011
COPY common/.build/actions-runner/part-012 /opt/qiniu-runner-build-cache/actions-runner/part-012
COPY common/.build/actions-runner/part-013 /opt/qiniu-runner-build-cache/actions-runner/part-013
COPY common/.build/actions-runner/part-014 /opt/qiniu-runner-build-cache/actions-runner/part-014
COPY common/.build/actions-runner/part-015 /opt/qiniu-runner-build-cache/actions-runner/part-015
RUN bash /usr/local/share/qiniu-sandbox-runner-template/assemble-runner-archive && \
    TEMPLATE_FLAVOR=slim RUNNER_TEMPLATE_PHASE=runtime bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh

ARG TEMPLATE_VERSION=20260917.3
ENV ImageVersion=$TEMPLATE_VERSION \
    IMAGE_VERSION=$TEMPLATE_VERSION

RUN set -eux; \
    environment_file=/etc/environment; \
    touch "$environment_file"; \
    sed -i -e '/^ImageOS=/d' -e '/^ImageVersion=/d' -e '/^IMAGE_VERSION=/d' \
      -e '/^IMAGE_TEMPLATE=/d' -e '/^RUNNER_TOOL_CACHE=/d' \
      -e '/^AGENT_TOOLSDIRECTORY=/d' "$environment_file"; \
    printf '%s\n' \
      "ImageOS=\"$ImageOS\"" \
      "ImageVersion=\"$TEMPLATE_VERSION\"" \
      "IMAGE_VERSION=\"$TEMPLATE_VERSION\"" \
      "IMAGE_TEMPLATE=\"$IMAGE_TEMPLATE\"" \
      "RUNNER_TOOL_CACHE=\"$RUNNER_TOOL_CACHE\"" \
      "AGENT_TOOLSDIRECTORY=\"$AGENT_TOOLSDIRECTORY\"" \
      >>"$environment_file"

RUN set -eux; \
    printf '%s\n' \
      'nameserver 1.1.1.1' \
      'nameserver 1.0.0.1' \
      >/etc/resolv.conf

USER runner
WORKDIR /home/runner/work
