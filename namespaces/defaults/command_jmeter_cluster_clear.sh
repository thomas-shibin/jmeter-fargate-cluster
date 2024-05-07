#!/usr/bin/env bash

working_dir=`pwd`

echo "checking if kubectl is present"

if ! hash kubectl 2>/dev/null
then
    echo "'kubectl' was not found in PATH"
    echo "Kindly ensure that you can acces an existing kubernetes cluster via kubectl"
    exit
fi

kubectl version --short

echo "Current list of namespaces on the kubernetes cluster:"

echo

kubectl get namespaces | grep -v NAME | awk '{print $1}'

echo

if [ -z "$1" ]; then
  echo "Enter the name of the new tenant unique name, this will be used to create the namespace"
  read tenant
  echo
else
  tenant="$1"
fi

if [ -n "$2" ]; then
  working_dir="$2"
fi

echo "Delete Jmeter slave nodes"

nodes=`kubectl get no | egrep -v "master|NAME" | wc -l`

echo

echo "Number of worker nodes on this cluster is " $nodes

echo

echo "Delete $nodes Jmeter slave replicas and service"

echo

kubectl delete -n $tenant -f $working_dir/jmeter_slaves_deploy.yaml

kubectl delete -n $tenant -f $working_dir/jmeter_slaves_svc.yaml

echo "Delete Jmeter Master"

kubectl delete -n $tenant -f $working_dir/jmeter_master_configmap.yaml

kubectl delete -n $tenant -f $working_dir/jmeter_master_deploy.yaml

sleep 20

kubectl delete ns $tenant
