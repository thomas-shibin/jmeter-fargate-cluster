#!/bin/bash

if [ -z "$1" ]; then
  working_dir="`pwd`"
else
  working_dir="$1"
fi

#Get namesapce variable
tenant=`awk '{print $NF}' "$working_dir/tenant_export"`

pod_ip=$(kubectl get pod -n "$tenant" -o wide | grep prometheus | grep Running | awk '{print $6}')

if [ -z "$pod_ip" ]; then
  echo "No running Prometheus pod found in namespace $tenant"
  exit 1
fi

echo $pod_ip