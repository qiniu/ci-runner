# syntax=docker/dockerfile:1.7
ARG BASE_PLATFORM=linux/amd64
FROM --platform=$BASE_PLATFORM public.ecr.aws/ubuntu/ubuntu:26.04@sha256:a5da6f6b18c3a4b8dcc73244592f7096f417d2667966d0e33460e9e308f25f67

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
ARG PESTER_NUPKG_SHA256=5a0fd80b361600bf4bbd4c307c1fd01b17f11668bab19e657add41b00ad22ab9
ARG GOOGLE_CLOUD_CLI_VERSION=577.0.0
ARG GOOGLE_CLOUD_CLI_ARCHIVE_SHA256=0b32d330446ce7b0f57f253e7efab4636c18fb1f87a3ac31c6c3f2a2a697525e
ARG NVM_VERSION=0.40.6
ARG NVM_ARCHIVE_SHA256=17302cad7feedb1ad33ba738f93d2176a90970724f22de119603624fcbdec1a2
ARG AWS_CLI_VERSION=2.36.3
ARG AWS_CLI_ARCHIVE_SHA256=512949c6175e7736e77761661d2e73809152c35914abd032ca00d4758e4041de
ARG AWS_SESSION_MANAGER_PLUGIN_VERSION=1.2.835.0
ARG AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256=7c6dcad12518571cc7959a713e6a8ae1bdf6ed66fd9bee37dc189e39ca58ae03
ARG AWS_SAM_CLI_VERSION=1.163.0
ARG AWS_SAM_CLI_ARCHIVE_SHA256=3f12863f45da82bc2ad1fb837444cca57450bd07fef73449b917362ef2f5ab70
ARG AWS_SAM_CLI_ARCHIVE_SIZE=93672108
ARG GITHUB_CLI_VERSION=2.96.0
ARG GITHUB_CLI_DEB_SHA256=11a731f4e0ca8c3db96ef6d2cc404dcab3d78247ce0e07c53e07117e7627d6a1
ARG YQ_VERSION=4.53.3
ARG YQ_BINARY_SHA256=fa52a4e758c63d38299163fbdd1edfb4c4963247918bf9c1c5d31d84789eded4
ARG ZSTD_VERSION=1.5.7
ARG ZSTD_ARCHIVE_SHA256=eb33e51f49a15e023950cd7825ca74a4a2b43db8354825ac24fc1b7ee09e6fa3
ARG NINJA_VERSION=1.13.2
ARG NINJA_ARCHIVE_SHA256=5749cbc4e668273514150a80e387a957f933c6ed3f5f11e03fb30955e2bbead6
ARG POWERSHELL_VERSION=7.6.4
ARG POWERSHELL_DEB_SHA256=e5688e0569568d48051c49d3e93504cde47af709cdaaabd9a8892bc676b3bdf3
ARG DOCKER_GPG_SHA256=1500c1f56fa9e26b9b8f42452a553675796ade0807cdce11975eb98170b3a570
ARG DOCKER_GPG_FINGERPRINT=9DC858229FC7DD38854AE2D88D81803C0EBFCD88

ENV DEBIAN_FRONTEND=noninteractive \
    ImageOS=ubuntu26 \
    IMAGE_TEMPLATE=github-runner-ubuntu-26-04 \
    RUNNER_TOOL_CACHE=/opt/hostedtoolcache \
    AGENT_TOOLSDIRECTORY=/opt/hostedtoolcache \
    HOME=/home/runner \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

COPY common/scripts/setup-common.sh /usr/local/share/qiniu-sandbox-runner-template/setup-common.sh
COPY github-runner-ubuntu-26.04/scripts/setup-template.sh /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
COPY common/scripts/ensure-docker /usr/local/share/qiniu-sandbox-runner-template/ensure-docker
COPY common/scripts/download-checked-range /usr/local/share/qiniu-sandbox-runner-template/download-checked-range
COPY common/scripts/curl /usr/local/share/qiniu-sandbox-runner-template/curl

RUN TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=bootstrap bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[0:7][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[7:8][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[8:14][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[14:15][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[15:18][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[18:21][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[21:22][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[22:23][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[23:24][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[24:27][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[27:28][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[28:30][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[30:32][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[32:33][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    patched_toolset="${toolset}.patched"; \
    jq '(.apt.common_packages, .apt.cmd_packages) |= map(if . == "netcat" then "netcat-openbsd" else . end)' \
      "$toolset" >"$patched_toolset"; \
    mv "$patched_toolset" "$toolset"; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[33:45][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[45:56][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[56:57][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    packages="$(jq -r '[.apt.common_packages[], .apt.cmd_packages[]] | .[57:][]' "$toolset")"; \
    test -n "$packages"; \
    apt-get install -y --no-install-recommends $packages
RUN set -eux; \
    toolset=/opt/qiniu-runner-images/images/ubuntu/scripts/build/toolset.json; \
    patched_toolset="${toolset}.patched"; \
    jq '(.apt.common_packages, .apt.cmd_packages) |= map(if . == "netcat-openbsd" then "netcat" else . end)' \
      "$toolset" >"$patched_toolset"; \
    mv "$patched_toolset" "$toolset"; \
    installer=/opt/qiniu-runner-images/images/ubuntu/scripts/build/install-apt-common.sh; \
    test "$(grep -Fxc '    apt-get install --no-install-recommends $package' "$installer")" -eq 1; \
    sed -i 's|    apt-get install --no-install-recommends \$package|    apt-get install --no-install-recommends ${package/netcat/netcat-openbsd}|' "$installer"; \
    test "$(grep -Fxc '    apt-get install --no-install-recommends ${package/netcat/netcat-openbsd}' "$installer")" -eq 1
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
    /opt/qiniu-runner-build-cache/aws-sam-cli.part-05 83886080 93672107
RUN set -eux; \
    cat /opt/qiniu-runner-build-cache/aws-sam-cli.part-* > /tmp/qiniu-aws-sam-cli.zip; \
    test "$(wc -c </tmp/qiniu-aws-sam-cli.zip | tr -d '[:space:]')" -eq "$AWS_SAM_CLI_ARCHIVE_SIZE"; \
    echo "$AWS_SAM_CLI_ARCHIVE_SHA256  /tmp/qiniu-aws-sam-cli.zip" | sha256sum --check -; \
    rm -f /opt/qiniu-runner-build-cache/aws-sam-cli.part-*; \
    rmdir /opt/qiniu-runner-build-cache; \
    TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=platform bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=node bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
RUN TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=toolchain bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh
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
    TEMPLATE_FLAVOR=versioned RUNNER_TEMPLATE_PHASE=runtime bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh

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
