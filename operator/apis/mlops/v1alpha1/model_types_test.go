package v1alpha1

import (
	"testing"

	. "github.com/onsi/gomega"
	scheduler "github.com/seldonio/seldon-core/scheduler/apis/mlops/scheduler"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAsModelDetails(t *testing.T) {
	g := NewGomegaWithT(t)
	type test struct {
		name    string
		model   *Model
		modelpb *scheduler.Model
		error   bool
	}
	replicas := int32(4)
	secret := "secret"
	modelType := "sklearn"
	server := "server"
	m1 := resource.MustParse("1M")
	m1bytes := uint64(1_000_000)
	incomeModel := "income"
	tests := []test{
		{
			name: "simple",
			model: &Model{
				ObjectMeta: metav1.ObjectMeta{
					Name:            "foo",
					Namespace:       "default",
					ResourceVersion: "1",
					Generation:      1,
				},
				Spec: ModelSpec{
					InferenceArtifactSpec: InferenceArtifactSpec{
						StorageURI: "gs://test",
					},
				},
			},
			modelpb: &scheduler.Model{
				Meta: &scheduler.MetaData{
					Name: "foo",
					KubernetesMeta: &scheduler.KubernetesMeta{
						Namespace:  "default",
						Generation: 1,
					},
				},
				ModelSpec: &scheduler.ModelSpec{
					Uri: "gs://test",
				},
				DeploymentSpec: &scheduler.DeploymentSpec{
					Replicas:    1,
					MinReplicas: 1,
				},
			},
		},
		{
			name: "complex",
			model: &Model{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "foo",
					Namespace:  "default",
					Generation: 1,
				},
				Spec: ModelSpec{
					InferenceArtifactSpec: InferenceArtifactSpec{
						ModelType:  &modelType,
						StorageURI: "gs://test",
						SecretName: &secret,
					},
					Logger:       &LoggingSpec{},
					Requirements: []string{"a", "b"},
					ScalingSpec:  ScalingSpec{Replicas: &replicas},
					Server:       &server,
					Explainer: &ExplainerSpec{
						Type:     "anchor_tabular",
						ModelRef: &incomeModel,
					},
					Parameters: []ParameterSpec{
						{
							Name:  "foo",
							Value: "bar",
						},
						{
							Name:  "foo2",
							Value: "bar2",
						},
					},
				},
			},
			modelpb: &scheduler.Model{
				Meta: &scheduler.MetaData{
					Name: "foo",
					KubernetesMeta: &scheduler.KubernetesMeta{
						Namespace:  "default",
						Generation: 1,
					},
				},
				ModelSpec: &scheduler.ModelSpec{
					Uri:           "gs://test",
					Requirements:  []string{"a", "b", modelType},
					StorageConfig: &scheduler.StorageConfig{Config: &scheduler.StorageConfig_StorageSecretName{StorageSecretName: secret}},
					Server:        &server,
					Explainer: &scheduler.ExplainerSpec{
						Type:     "anchor_tabular",
						ModelRef: &incomeModel,
					},
					Parameters: []*scheduler.ParameterSpec{
						{
							Name:  "foo",
							Value: "bar",
						},
						{
							Name:  "foo2",
							Value: "bar2",
						},
					},
				},
				DeploymentSpec: &scheduler.DeploymentSpec{
					Replicas:    4,
					LogPayloads: true,
					MinReplicas: 1,
				},
			},
		},
		{
			name: "memory",
			model: &Model{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "foo",
					Namespace:  "default",
					Generation: 1,
				},
				Spec: ModelSpec{
					InferenceArtifactSpec: InferenceArtifactSpec{
						StorageURI: "gs://test",
					},
					Memory: &m1,
				},
			},
			modelpb: &scheduler.Model{
				Meta: &scheduler.MetaData{
					Name: "foo",
					KubernetesMeta: &scheduler.KubernetesMeta{
						Namespace:  "default",
						Generation: 1,
					},
				},
				ModelSpec: &scheduler.ModelSpec{
					Uri:         "gs://test",
					MemoryBytes: &m1bytes,
				},
				DeploymentSpec: &scheduler.DeploymentSpec{
					Replicas:    1,
					MinReplicas: 1,
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			md, err := test.model.AsSchedulerModel()
			if !test.error {
				g.Expect(err).To(BeNil())
				g.Expect(md).To(Equal(test.modelpb))
			} else {
				g.Expect(err).ToNot(BeNil())
			}
		})
	}
}