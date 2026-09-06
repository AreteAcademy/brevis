// Módulo PRÓPRIO, e não um pacote da raiz: um fetcher importa o SDK, e o SDK
// traz BigQuery, S3 e MySQL atrás. Pendurá-lo no go.mod do motor arrastaria
// essa árvore para o módulo que peso-do-motor.sh existe para manter magro.
module fetcherdeteste

go 1.23.0

require github.com/AreteAcademy/brevis/sdk v0.0.0

require github.com/google/uuid v1.6.0 // indirect

replace github.com/AreteAcademy/brevis/sdk => ../../../../../sdk
