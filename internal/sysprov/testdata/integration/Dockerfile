# A throwaway Debian for the sysprov integration test.
#
# It is bookworm because that is what the Waypoint image is built from, and it
# carries exactly the packages the helper shells out to — no Go toolchain, because
# run.sh compiles the test on the host and mounts the binary in. That keeps the
# image small and, more usefully, means the test runs against the same package
# versions a node has rather than against whatever a golang image ships.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      passwd \
      login \
      hostname \
      openssh-server \
      sudo \
      systemd \
      strace \
 && rm -rf /var/lib/apt/lists/*

# sshd refuses to run without host keys or its privilege-separation directory,
# and the test uses sshd -t and sshd -T to prove the drop-ins produce a
# configuration sshd actually accepts. On a node systemd's ssh.service creates
# /run/sshd through RuntimeDirectory=; nothing does that here.
RUN ssh-keygen -A && mkdir -p /run/sshd

# Give root a password so `passwd --status root` starts at P and the test can see
# LockRoot change it to L. A fresh Debian container has root at NP, which would
# make the transition invisible.
RUN echo 'root:integration-only-not-a-secret' | chpasswd

# The syscall check needs the unit's filter resolved by the same systemd that
# would enforce it, and strace to see what the helper actually calls.
COPY verify-syscalls.sh /verify-syscalls.sh
RUN chmod +x /verify-syscalls.sh
