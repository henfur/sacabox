# Debian dev VM with one project directory shared in.   vm sh dev
{ ... }:

{
  services.sacabox.machines.dev = {
    template = "dev";
    shares.projects.host = "/home/you/projects";
    init.text = ''
      apt-get update -qq
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git build-essential curl
    '';
  };
}
