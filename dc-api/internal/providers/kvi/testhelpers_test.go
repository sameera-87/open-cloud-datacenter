package kvi_test

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Small zero-value option constructors so the test files read cleanly
// without sprinkling `metav1.GetOptions{}` everywhere.
func metav1Get() metav1.GetOptions       { return metav1.GetOptions{} }
func metav1Create() metav1.CreateOptions { return metav1.CreateOptions{} }
