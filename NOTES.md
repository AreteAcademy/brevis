3. Precisamos dar suporte para NodeJs, um comunidade que não usa muito NodeJs para engenharia, mas é uma linguagem flexivel e que pode sim ser usada para criar algum processos de elt diferenciados, pensando nisso, precisamos montar um npm que o usuario consiga ler o contexto, igual fizemos em Python porem em NodeJs.
  
import { BrevisContext } from '@brevis';  

const bucket = BrevisContext.get('bucket')  
console.log(bucket)

BrevisContext.set({ bucket: 'cgs://' })  
  
com base nisso analise como podemos realizar isso, e monte um plano para que  
tenhamos sucesso com isso, se fizermos dessa forma teremos uma comunidade forte em NodeJs que possa usar o ecosistema. salve o plano em ingles em docs.

Como podemos fazer com que esse sdk aqui se comunique com a API??
  
Lembrando que esse contexto precisa passar pela estrutura, para que possamos recuperar o contexto caso falhe, o contexto precisa estar salvo no banco de dados, e toda execucao precisa ter um ID que identifica aquele sessao, e cada run ter o seu id

4. Precisamos montar um pod chamado alert, da mesma forma que fizemos com a api e scheule, esse alert vai ter como report via slack, dessa forma deixamos mais organizado o projeto, e cada step ira poder enviar alertas de erro para o slack, e o fluxo precisa aparecer dentro da UI, vamos supor que temos steps, vamos ter um chamado erro: com a config para alertar quando houver um erro, vai ter um type, pode hora sera slack, mas no fututo iremos usar outros, entao o valor sera SLACK, e ele deve depender na env que ja usamos hoje, o cliente precisa configurar o slack para isso, outro detalhe interessante, acho que faz mais sentido, vamos montar chamada report, com os types ALERT ou INSIGHTS, quando for alert ele reproduz com base em erros, e report sera um dispatch configura onde iremos enviar semanalmente um report, de sucesso, falhas, todas de bytes, totoal de linhas, tempo maximo, medio e minimo, se possivel insights de recursos da infra. com base nesse contexto monte um plano para executar com sucesso.

5. Precisamos evoluir a observabilidade desse nosso projeto, precisamos expor essa observabilidade para o consumidor, precisamos ter nativamente metricas ta aplicacao com um todo, com base noque ja contruimos no brevis, preciso que vc me ajuda a montar isso, precisa ser no padrao otl, para que seja possivel ter um scraping para extrair as metricas, e o sdk deve dar suport para montar novas metricas sdk.metics, e preciso que vc me ajude a montar da melhor forma possivel, seguindo as melhores praticas de mercado para dar suporte para openTelemetry, monte um plano supor consistente para que tenhamos sucesso nessa implementacao.

6. Precisamos conseguir criar flow de diversas forma, dependentes, paralelos, condicionais if erro e if sucess, sub fluxos, e os mais diversos tipos de fluxos, preciso que vc avalise isso, trazendo as melhores praticas que e usada pelo Airflow e N8N que realizam isso com maestria

7. Precisamos montar imagens customizadas para diversos Jobs, uma imagem para rodar transformacoes em Pandas ou Polaris, imagem GO ultra leve para rodar o sdk, imagem performatica para rodar somente o DBT, pensei que podemos ter imagens montadas por nosa para que possamos disponibilizar. algo assim brevis/etl-go:1.4, brevis/etl-python-analytics-pandas:1.4, brevis/etl-python-analytics-polaris:1.4, brevis/etl-python:1.4, brevis/etl-node:1.4 cada imagem segue uma receita nossa, e ambas ja trazem o nosso sdk embutido, conforme vamos trazendo mais linguagem para o nosso ecosistema iremos montendo as que temos suporte.

8. Uma das proposta do brevis é ele ser um orquestrador de ETL, e temos oportunidades de orquestrar EKS(Pod), GKE(Pod), CloudRun, AWS Lambda, ECS, EC2, Machine GCP, pensnado dessa forma, preciso que vc monte um plano de acao para atacarmos, monte um plano consistente e maduro, vamos usar o floci.io (https://floci.io), para simular o nosso ambiente inteiro, claro, até onde conseguirmos para validar todo o nosso ecosistema.

10. Quandi fizermos o carregamento dos dados, precisamos realizar o evolution schema para todos os from, quando um cliente/consumidor usar a nossa solucao, ele precisa ter esse mecanismo, preciso que com base noque o mercado, e como usa, preciso que vc monte um plano comtemplando as melhores praticas de mercado e das melhoras empresas, siga oque as melhores referencia usam, pode montar como um espelho oque se faz no mercado.

