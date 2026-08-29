// CI/CD del API en la Raspberry Pi.
//
// El job corre en el agente `rpi`: un nodo SSH que entra al propio host de la
// Pi. Eso importa, porque el contenedor de Jenkins usa un daemon Docker aislado
// (DinD) y un `docker compose` lanzado ahí dejaría los contenedores dentro del
// DinD en vez de en la Pi.
//
// El workspace ES el directorio de despliegue (~/Projects/japo-backend): un
// solo checkout hace de fuente y de raíz del stack, igual que si desplegaras a
// mano. Por eso el job no debe limpiar el workspace.
pipeline {
    agent {
        node {
            label 'rpi'
            customWorkspace '/home/jampueroc/Projects/japo-backend'
        }
    }

    options {
        disableConcurrentBuilds()
        timestamps()
        buildDiscarder(logRotator(numToKeepStr: '20'))
        timeout(time: 45, unit: 'MINUTES')
    }

    parameters {
        booleanParam(
            name: 'RUN_TESTS',
            defaultValue: true,
            description: 'Correr los tests unitarios en un contenedor Go efímero antes de desplegar.'
        )
    }

    environment {
        // Los secretos viven fuera del repo y fuera del workspace: nunca llegan
        // a GitHub y un checkout limpio no los borra.
        ENV_FILE = '/home/jampueroc/Projects/japo-secrets/backend.env'
    }

    stages {
        stage('Preflight') {
            steps {
                sh '''
                    set -eu
                    test -f "$ENV_FILE" || {
                        echo "Falta el fichero de entorno $ENV_FILE"
                        echo "Créalo a partir de .env.example (ver README de despliegue)."
                        exit 1
                    }
                    docker version >/dev/null
                    docker compose version >/dev/null
                '''
            }
        }

        stage('Test') {
            when { expression { return params.RUN_TESTS } }
            steps {
                // El toolchain de Go no está instalado en la Pi: los tests corren
                // en un contenedor efímero. El volumen cachea los módulos para que
                // la segunda ejecución no vuelva a descargar todo.
                sh '''
                    set -eu
                    docker run --rm \
                        -v "$PWD":/src \
                        -w /src \
                        -v japo-go-modcache:/go/pkg/mod \
                        -v japo-go-buildcache:/root/.cache/go-build \
                        golang:1.25-alpine \
                        go test ./...
                '''
            }
        }

        stage('Build & Deploy') {
            steps {
                sh '''
                    set -eu
                    # Etiqueta trazable: número de build + commit desplegado.
                    VERSION="b${BUILD_NUMBER}-$(git rev-parse --short HEAD)"
                    export VERSION
                    echo "Desplegando versión $VERSION"

                    docker compose --env-file "$ENV_FILE" up -d --build --remove-orphans
                '''
            }
        }

        stage('Health check') {
            steps {
                // El Dockerfile ya define un HEALTHCHECK que pega a /health, y ese
                // endpoint hace ping a MariaDB: si pasa, el stack entero está vivo.
                sh '''
                    set -eu
                    for i in $(seq 1 40); do
                        status=$(docker inspect -f '{{.State.Health.Status}}' japo-api 2>/dev/null | tail -n1)
                        status=${status:-missing}
                        case "$status" in
                            healthy) echo "japo-api healthy"; exit 0 ;;
                            unhealthy) echo "japo-api unhealthy"; break ;;
                        esac
                        sleep 3
                    done
                    echo "El API no llegó a healthy. Últimos logs:"
                    docker compose --env-file "$ENV_FILE" logs --tail=120 api mariadb
                    exit 1
                '''
            }
        }

        stage('Prune') {
            steps {
                // Las imágenes intermedias se acumulan rápido en una Pi. Sólo se
                // borra lo que ya no referencia ningún contenedor.
                sh 'docker image prune -f >/dev/null'
            }
        }
    }

    post {
        failure {
            sh '''
                docker compose --env-file "$ENV_FILE" ps || true
                docker compose --env-file "$ENV_FILE" logs --tail=120 || true
            '''
        }
    }
}
